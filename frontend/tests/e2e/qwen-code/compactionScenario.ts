import type { NativeCompactionOptions } from '../helpers/manualCompaction'

/**
 * The native compaction of Qwen Code. Its summarizer request falls through to the fallback, which answers the summary.
 * Qwen Code reads the summary from its own XML envelope, and it compares the reported history size with the summary,
 * so the seed turns report their input tokens.
 */
export const QWEN_COMPACTION: NativeCompactionOptions = {
  summary: { route: 'fallback', requestMarker: 'You are the component that summarizes a conversation' },
  summaryEnvelope: summary => `<analysis>The conversation needs a checkpoint.</analysis><state_snapshot><current_work>${summary}</current_work><next_step>Continue the user's work.</next_step></state_snapshot>`,
  reportedInputTokens: 10_000,
}
