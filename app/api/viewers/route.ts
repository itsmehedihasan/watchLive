import { NextResponse } from 'next/server';
import type { NextRequest } from 'next/server';

interface SessionData {
  ts: number;
  channelId: string | null;
}

// sessionId → { ts, channelId }
const sessions = new Map<string, SessionData>();
// channelId → total tune-in events (increments only when a session switches to a new channel)
const viewCounts = new Map<string, number>();
const TTL = 60_000;

function prune() {
  const cutoff = Date.now() - TTL;
  for (const [id, data] of sessions) {
    if (data.ts < cutoff) sessions.delete(id);
  }
}

function channelCount(channelId: string): number {
  let n = 0;
  for (const data of sessions.values()) {
    if (data.channelId === channelId) n++;
  }
  return n;
}

function topChannels(n: number): Array<{ id: string; count: number }> {
  return [...viewCounts.entries()]
    .sort((a, b) => b[1] - a[1])
    .slice(0, n)
    .map(([id, count]) => ({ id, count }));
}

export async function POST(req: Request) {
  let channelId: string | null = null;
  try {
    const body = await req.json() as { sessionId?: unknown; channelId?: unknown };
    if (typeof body.sessionId === 'string' && body.sessionId) {
      channelId = typeof body.channelId === 'string' && body.channelId ? body.channelId : null;
      prune();
      const prev = sessions.get(body.sessionId);
      // count each distinct tune-in (session switches to a new channel)
      if (channelId !== null && prev?.channelId !== channelId) {
        viewCounts.set(channelId, (viewCounts.get(channelId) ?? 0) + 1);
      }
      sessions.set(body.sessionId, { ts: Date.now(), channelId });
    }
  } catch {
    // malformed body — still return counts
  }
  prune();
  return NextResponse.json({
    total: sessions.size,
    channelCount: channelId !== null ? channelCount(channelId) : null,
    top: topChannels(5),
  });
}

export async function GET(req: NextRequest) {
  const cid = req.nextUrl.searchParams.get('channelId');
  const n = parseInt(req.nextUrl.searchParams.get('top') ?? '5', 10);
  prune();
  return NextResponse.json({
    total: sessions.size,
    channelCount: cid ? channelCount(cid) : null,
    top: topChannels(n),
  });
}
