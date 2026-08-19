import { pickString } from '~/lib/jsonPick'

/**
 * The `ListAgents` listing: the CLI's own `listing` field, falling back to the
 * block text.
 *
 * Structured first, text second -- the documented idiom. `listing` is what the
 * CLI's `mapToolResultToToolResultBlockParam` puts into the tool_result content,
 * so the two are the same string today and the fallback covers a transcript
 * recorded before the structured payload existed.
 *
 * One home for the rule, because three callers ask about the same text and must
 * agree: the card renders it, the toolbar decides collapsibility from it, and
 * Copy yields it. A second copy is how "the toolbar acts on what the user sees"
 * stops being true.
 */
export function claudeListAgentsListing(
  toolUseResult: Record<string, unknown> | undefined,
  resultContent: string,
): string {
  return pickString(toolUseResult, 'listing', '') || resultContent
}
