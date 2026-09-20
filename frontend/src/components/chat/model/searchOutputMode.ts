/**
 * How a grep reports what it found.
 *
 * These are Grep's own `output_mode` words, and five providers pass the tool input
 * straight through to them. The set is CLOSED because a shared renderer compares
 * against it: `SearchResultBody` shows the match count only in `count` mode, so a
 * provider that spelled a fourth word -- or a renderer that compared against a typo --
 * silently dropped that summary with nothing to say why.
 */
export const SEARCH_OUTPUT_MODES = ['content', 'files_with_matches', 'count'] as const

export type SearchOutputMode = (typeof SEARCH_OUTPUT_MODES)[number]

const KNOWN_MODES: ReadonlySet<string> = new Set(SEARCH_OUTPUT_MODES)

/**
 * Narrow a tool's `output_mode` to one mode, or undefined for anything else.
 *
 * Undefined is the right answer for a word no release declared: the row then reports
 * what it found without claiming a mode, which is what a search that states none does
 * already.
 */
export function searchOutputMode(value: unknown): SearchOutputMode | undefined {
  return typeof value === 'string' && KNOWN_MODES.has(value) ? value as SearchOutputMode : undefined
}
