import type { NativeCompactionOptions } from '../helpers/manualCompaction'

/** The native compaction of ZCode. Its summarizer takes the next ordered step. */
export const ZCODE_COMPACTION: NativeCompactionOptions = {
  summary: { route: 'queued' },
}

/**
 * The trigger that the compaction lifecycle event reports for a user `/compact`.
 *
 * The completed notice states it in its detail, so the detail is what tells the
 * event row from the plain row of an older stored build.
 */
export const ZCODE_MANUAL_COMPACTION_TRIGGER = 'manual'
