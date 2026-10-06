import type { NativeCompactionOptions } from '../helpers/manualCompaction'

/** The native compaction of Kimi Code. Its summarizer request falls through to the fallback, which answers the summary. */
export const KIMI_COMPACTION: NativeCompactionOptions = {
  summary: { route: 'fallback', requestMarker: 'You are about to run out of context' },
}
