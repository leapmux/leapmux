import type { NativeCompactionOptions } from '../helpers/manualCompaction'

/** The native compaction of Grok Build. Its summarizer request falls through to the fallback, which answers the summary. */
export const GROK_COMPACTION: NativeCompactionOptions = {
  summary: { route: 'fallback', requestMarker: 'Your task is to produce a faithful, concise summary of the conversation so far' },
}
