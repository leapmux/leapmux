import type { NativeCompactionOptions } from '../helpers/manualCompaction'

/**
 * The native compaction of Claude Code.
 * `/compact` is Claude's own slash command. Its summarizer is a housekeeping turn that can run more than once, so a
 * rule answers it. The last user turn holds the prior prompt before the native directive, so the rule matches the
 * directive inside that turn. The notice comes from the `compact_boundary` system message of the CLI.
 */
export const CLAUDE_COMPACTION: NativeCompactionOptions = {
  summary: { route: 'rule', when: { user: 'CRITICAL: Respond with TEXT ONLY\\.' } },
}
