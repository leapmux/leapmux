import type { NativeCompactionOptions } from '../helpers/manualCompaction'

/**
 * The native compaction of Kiro. Its summarizer request falls through to the fallback, which answers the summary.
 * Kiro sends its service request as an event stream that the proof cannot search, so the completion message of the
 * transcript proves the summary.
 */
export const KIRO_COMPACTION: NativeCompactionOptions = {
  summary: { route: 'fallback', completionText: 'Context compacted' },
}
