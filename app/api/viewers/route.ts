import { NextResponse } from 'next/server';

// In-memory session store: sessionId -> last-seen timestamp (ms)
const sessions = new Map<string, number>();
const TTL = 60_000; // drop sessions silent for >60s

function prune() {
  const cutoff = Date.now() - TTL;
  for (const [id, ts] of sessions) {
    if (ts < cutoff) sessions.delete(id);
  }
}

export async function POST(req: Request) {
  try {
    const { sessionId } = await req.json() as { sessionId?: unknown };
    if (typeof sessionId === 'string' && sessionId) {
      prune();
      sessions.set(sessionId, Date.now());
    }
  } catch {
    // malformed body — still return current count
  }
  return NextResponse.json({ count: sessions.size });
}

export async function GET() {
  prune();
  return NextResponse.json({ count: sessions.size });
}
