// Local in-memory storage with TTL support
interface StorageValue {
  value: unknown;
  expiresAt?: number;
}

interface SortedSetMember {
  member: string;
  score: number;
}

class LocalStorage {
  private store = new Map<string, StorageValue>();
  private cleanupInterval: NodeJS.Timeout | null = null;

  constructor() {
    // Cleanup expired keys every 10 seconds
    this.cleanupInterval = setInterval(() => this.cleanup(), 10000);
  }

  private isExpired(value: StorageValue): boolean {
    return value.expiresAt !== undefined && value.expiresAt < Date.now();
  }

  private cleanup(): void {
    const now = Date.now();
    for (const [key, value] of this.store.entries()) {
      if (value.expiresAt !== undefined && value.expiresAt < now) {
        this.store.delete(key);
      }
    }
  }

  async set<T>(
    key: string,
    value: T,
    options?: { ex?: number; get?: boolean }
  ): Promise<T | null> {
    const oldValue = this.store.get(key);
    const result = oldValue && !this.isExpired(oldValue) ? (oldValue.value as T) : null;

    const expiresAt = options?.ex ? Date.now() + options.ex * 1000 : undefined;
    this.store.set(key, { value, expiresAt });

    return options?.get ? result : null;
  }

  async get<T>(key: string): Promise<T | null> {
    const value = this.store.get(key);
    if (!value) return null;
    if (this.isExpired(value)) {
      this.store.delete(key);
      return null;
    }
    return value.value as T;
  }

  async hset(key: string, fields: Record<string, unknown>): Promise<number> {
    const current = (this.store.get(key)?.value as Record<string, unknown>) || {};
    const updated = { ...current, ...fields };
    this.store.set(key, { value: updated, expiresAt: this.store.get(key)?.expiresAt });
    return Object.keys(fields).length;
  }

  async expire(key: string, seconds: number): Promise<number> {
    const value = this.store.get(key);
    if (!value) return 0;
    value.expiresAt = Date.now() + seconds * 1000;
    return 1;
  }

  async zadd(
    key: string,
    members: { score: number; member: string } | { score: number; member: string }[]
  ): Promise<number> {
    const current = ((this.store.get(key)?.value as SortedSetMember[]) || []).slice();
    const memberArray = Array.isArray(members) ? members : [members];

    for (const { score, member } of memberArray) {
      const idx = current.findIndex((m) => m.member === member);
      if (idx >= 0) {
        current[idx].score = score;
      } else {
        current.push({ member, score });
      }
    }

    this.store.set(key, { value: current, expiresAt: this.store.get(key)?.expiresAt });
    return memberArray.length;
  }

  async zincrby(key: string, increment: number, member: string): Promise<number> {
    const current = ((this.store.get(key)?.value as SortedSetMember[]) || []).slice();
    const idx = current.findIndex((m) => m.member === member);
    const newScore = idx >= 0 ? current[idx].score + increment : increment;

    if (idx >= 0) {
      current[idx].score = newScore;
    } else {
      current.push({ member, score: newScore });
    }

    this.store.set(key, { value: current, expiresAt: this.store.get(key)?.expiresAt });
    return newScore;
  }

  async zcard(key: string): Promise<number> {
    const value = this.store.get(key);
    if (!value || this.isExpired(value)) return 0;
    return ((value.value as SortedSetMember[]) || []).length;
  }

  async zrange(
    key: string,
    start: number,
    stop: number,
    options?: { rev?: boolean; withScores?: boolean }
  ): Promise<string[] | [string, number][]> {
    const value = this.store.get(key);
    if (!value || this.isExpired(value)) return [];

    let members = ((value.value as SortedSetMember[]) || []).slice();
    if (options?.rev) {
      members.sort((a, b) => b.score - a.score);
    } else {
      members.sort((a, b) => a.score - b.score);
    }

    const sliced = members.slice(start, stop + 1);
    if (options?.withScores) {
      return sliced.map((m) => [m.member, m.score]);
    }
    return sliced.map((m) => m.member);
  }

  async zremrangebyscore(key: string, min: number, max: number): Promise<number> {
    const value = this.store.get(key);
    if (!value || this.isExpired(value)) return 0;

    const current = (value.value as SortedSetMember[]) || [];
    const before = current.length;
    const filtered = current.filter((m) => m.score < min || m.score > max);
    this.store.set(key, { value: filtered, expiresAt: value.expiresAt });
    return before - filtered.length;
  }

  async incr(key: string): Promise<number> {
    const value = this.store.get(key)?.value ?? 0;
    const newValue = (typeof value === 'number' ? value : parseInt(String(value), 10) || 0) + 1;
    this.store.set(key, { value: newValue, expiresAt: this.store.get(key)?.expiresAt });
    return newValue;
  }

  async decr(key: string): Promise<number> {
    const value = this.store.get(key)?.value ?? 0;
    const newValue = (typeof value === 'number' ? value : parseInt(String(value), 10) || 0) - 1;
    this.store.set(key, { value: newValue, expiresAt: this.store.get(key)?.expiresAt });
    return newValue;
  }

  pipeline() {
    const commands: Array<{ method: string; args: unknown[] }> = [];

    return {
      hset: (...args: unknown[]) => {
        commands.push({ method: 'hset', args });
        return this;
      },
      expire: (...args: unknown[]) => {
        commands.push({ method: 'expire', args });
        return this;
      },
      zadd: (...args: unknown[]) => {
        commands.push({ method: 'zadd', args });
        return this;
      },
      zremrangebyscore: (...args: unknown[]) => {
        commands.push({ method: 'zremrangebyscore', args });
        return this;
      },
      decr: (...args: unknown[]) => {
        commands.push({ method: 'decr', args });
        return this;
      },
      zincrby: (...args: unknown[]) => {
        commands.push({ method: 'zincrby', args });
        return this;
      },
      incr: (...args: unknown[]) => {
        commands.push({ method: 'incr', args });
        return this;
      },
      zcard: (...args: unknown[]) => {
        commands.push({ method: 'zcard', args });
        return this;
      },
      get: (...args: unknown[]) => {
        commands.push({ method: 'get', args });
        return this;
      },
      zrange: (...args: unknown[]) => {
        commands.push({ method: 'zrange', args });
        return this;
      },
      exec: async () => {
        const results = [];
        for (const cmd of commands) {
          const method = cmd.method as keyof LocalStorage;
          results.push(await (this as any)[method](...cmd.args));
        }
        return results;
      },
    };
  }
}

export const redis = new LocalStorage();
