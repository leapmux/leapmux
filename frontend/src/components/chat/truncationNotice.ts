/**
 * What LeapMux says when a row shows only PART of what a tool produced.
 *
 * One notice, because a reader who works across providers should learn it once. The
 * three render paths each spelled it themselves, and a live truncation census read two
 * spellings for one state on the same operation: OpenCode's capped search said
 * "Output truncated" and Pi's said "[output truncated]", because one ran a native
 * search tool and the other ran a shell command.
 *
 * A provider that explains its own limit keeps its words. This is the fallback for a
 * provider that reports the cut without describing it, and it sits beside
 * `toolOutcomeLabel` for the same reason.
 */
export const TRUNCATION_NOTICE = 'Output truncated'
