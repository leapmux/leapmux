import type { NativeCompactionOptions } from '../helpers/manualCompaction'

/**
 * The native compaction of MiMo Code. Its summarizer request falls through to the fallback, which answers the summary.
 *
 * MiMo Code 0.1.15 sends its compaction part twice: once when the compaction starts, and once when it ends, with the
 * summary in the projection of the part. The Worker stores both as notifications, and the transcript folds the start
 * into the end, which it draws as the completed notice.
 */
export const MIMO_COMPACTION: NativeCompactionOptions = {
  summary: { route: 'fallback', requestMarker: 'Write a continuation summary that will allow you' },
}
