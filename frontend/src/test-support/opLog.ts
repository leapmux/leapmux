import { appendOpLogSegment } from '~/lib/crdt/checkpointStore'

/**
 * Append an op-log segment with the next per-owner ordinal, the way the
 * production recorder does.
 *
 * `appendOpLogSegment` takes the ordinal as a required argument rather than
 * assigning one itself: it is a per-OWNER sequence and the store would have to
 * read the log back to derive it, which is the read-modify-write the segment
 * design exists to avoid. The recorder holds the counter instead.
 *
 * Tests that hand-pick ordinals (to plant a hole, or to assert the read stops at
 * one) should call `appendOpLogSegment` directly. This helper is for the many
 * that only need a well-formed, contiguous log.
 */
export function createOpLogAppender() {
  const next = new Map<string, number>()
  return {
    /** Append `frames` as the next contiguous segment for this owner. */
    append(userId: string, clientId: string, frames: Uint8Array[]): Promise<void> {
      // A space separator, written literally -- never a control byte. A NUL
      // would make git treat this file as binary and hide its diffs (see
      // noControlBytesInSource.test.ts). Any separator that cannot occur in
      // either id works; it keeps ('ab','c') and ('a','bc') distinct owners.
      const key = `${userId} ${clientId}`
      const ordinal = next.get(key) ?? 0
      next.set(key, ordinal + 1)
      return appendOpLogSegment(userId, clientId, frames, ordinal)
    },
    /**
     * Forget every owner's position — mirrors a checkpoint write, which
     * truncates the log and restarts the sequence at 0.
     */
    reset(): void {
      next.clear()
    },
  }
}
