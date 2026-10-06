import type { NativeCompactionOptions } from '../helpers/manualCompaction'

/**
 * The native compaction of OpenCode, and of Kilo, which builds on OpenCode and sends the same summarizer prompt.
 * The summarizer request falls through to the fallback, which answers the summary.
 */
export const OPENCODE_COMPACTION: NativeCompactionOptions = {
  summary: { route: 'fallback', requestMarker: 'Create a new anchored summary from the conversation history' },
}
