import { NextResponse } from 'next/server';
import type { NextRequest } from 'next/server';
import { redis } from '@/lib/redis';

const SESSION_TTL = 60;  // seconds
const CHANNEL_TTL = 90;  // wider than session TTL to avoid negative counts on race

// @upstash/redis returns ZRANGE WITHSCORES as a flat [member, score, ...] array
function parseZrangeWithScores(raw: unknown): Array<{ id: string; count: number }> {
  if (!Array.isArray(raw)) return [];
  const out: Array<{ id: string; count: number }> = [];
  for (let i = 0; i < raw.length; i += 2) {
    const id = String(raw[i]);
    const count = Number(raw[i + 1]);
    if (id) out.push({ id, count });
  }
  return out;
}

export async function POST(req: Request) {
  let channelId: string | null = null;

  try {
    const body = await req.json() as { sessionId?: unknown; channelId?: unknown };
    const sid = typeof body.sessionId === 'string' && body.sessionId ? body.sessionId : null;
    if (!sid) throw new Error('no sessionId');

    channelId = typeof body.channelId === 'string' && body.channelId ? body.channelId : null;

    const nowSeconds = Math.floor(Date.now() / 1000);
    const sessionKey = `session:${sid}`;
    const sessionChanKey = `session:channel:${sid}`;

    // Atomic SET GET: sets new value and returns old value in one round-trip.
    // Prevents two concurrent heartbeats from both seeing null (race condition).
    const prevChannel = await redis.set<string>(sessionChanKey, channelId ?? '', { ex: SESSION_TTL, get: true });
    const isNewSession = prevChannel === null;
    const channelChanged = !isNewSession && prevChannel !== (channelId ?? '');

    const p = redis.pipeline();

    p.hset(sessionKey, { channelId: channelId ?? '', ts: Date.now() });
    p.expire(sessionKey, SESSION_TTL);
    // Track session in sorted set (score = last-seen timestamp).
    // ZREMRANGEBYSCORE prunes sessions older than SESSION_TTL so ZCARD is always accurate.
    p.zadd('session:active', { score: nowSeconds, member: sid });
    p.zremrangebyscore('session:active', 0, nowSeconds - SESSION_TTL);

    if (channelChanged || isNewSession) {
      if (!isNewSession && prevChannel) {
        p.decr(`channel:viewers:${prevChannel}`);
      }
      if (channelId) {
        p.zincrby('tunein:counts', 1, channelId);
        p.incr(`channel:viewers:${channelId}`);
        p.expire(`channel:viewers:${channelId}`, CHANNEL_TTL);
      }
    } else if (channelId) {
      p.expire(`channel:viewers:${channelId}`, CHANNEL_TTL);
    }

    await p.exec();
  } catch {
    // malformed body or Redis error — fall through to return current counts
  }

  const readPipe = redis.pipeline();
  readPipe.zcard('session:active');
  if (channelId) readPipe.get(`channel:viewers:${channelId}`);
  readPipe.zrange('tunein:counts', 0, 4, { rev: true, withScores: true });

  const results = await readPipe.exec();

  const total = Math.max(0, (results[0] as number | null) ?? 0);
  let channelCount: number | null = null;
  let topRaw: unknown;

  if (channelId) {
    channelCount = Math.max(0, parseInt((results[1] as string | null) ?? '0', 10) || 0);
    topRaw = results[2];
  } else {
    topRaw = results[1];
  }

  return NextResponse.json({
    total,
    channelCount,
    top: parseZrangeWithScores(topRaw),
  });
}

export async function GET(req: NextRequest) {
  const cid = req.nextUrl.searchParams.get('channelId');
  const n = Math.min(parseInt(req.nextUrl.searchParams.get('top') ?? '5', 10), 20);

  const readPipe = redis.pipeline();
  readPipe.zcard('session:active');
  if (cid) readPipe.get(`channel:viewers:${cid}`);
  readPipe.zrange('tunein:counts', 0, n - 1, { rev: true, withScores: true });

  const results = await readPipe.exec();

  const total = Math.max(0, (results[0] as number | null) ?? 0);
  let channelCount: number | null = null;
  let topRaw: unknown;

  if (cid) {
    channelCount = Math.max(0, parseInt((results[1] as string | null) ?? '0', 10) || 0);
    topRaw = results[2];
  } else {
    topRaw = results[1];
  }

  return NextResponse.json({
    total,
    channelCount,
    top: parseZrangeWithScores(topRaw),
  });
}
