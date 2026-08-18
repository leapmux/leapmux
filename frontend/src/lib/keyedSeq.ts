/**
 * A monotonic sequence per key, plus the test that one sequence is still
 * the newest its key handed out.
 *
 * Every call site holds the same rule: a request stamps a sequence when it
 * is ISSUED, and its reply applies only while no later request took the
 * same key. Without the rule, two writes to one key that finish out of
 * order leave the OLDER value on screen — two fast clicks on a toggle, and
 * it snaps back.
 *
 * The key is OPTIONAL. A caller that owns one subject (a single settings
 * row, one load path) needs one counter, and an absent key gives it
 * exactly that instead of a constant it has to invent.
 */
export interface KeyedSeq {
  /** Take the next sequence for `key` and make it the newest one. */
  next: (key?: string) => number
  /**
   * Whether `seq` is still the newest sequence for `key`.
   *
   * A key that never handed one out reads as 0, so a caller can compare a
   * snapshot taken before it asked (see `snapshot`) without a special case
   * for the keys that snapshot did not hold.
   */
  isNewest: (key: string | undefined, seq: number) => boolean
  /** A copy of every sequence taken so far, by key. */
  snapshot: () => ReadonlyMap<string, number>
}

/** The key an unkeyed caller uses: one counter for the whole helper. */
const UNKEYED = ''

export function createKeyedSeq(): KeyedSeq {
  const seqs = new Map<string, number>()
  return {
    next: (key = UNKEYED) => {
      const seq = (seqs.get(key) ?? 0) + 1
      seqs.set(key, seq)
      return seq
    },
    isNewest: (key, seq) => (seqs.get(key ?? UNKEYED) ?? 0) === seq,
    snapshot: () => new Map(seqs),
  }
}
