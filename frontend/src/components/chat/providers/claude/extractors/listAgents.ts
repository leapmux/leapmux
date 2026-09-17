import type { ToolCallPayload } from '../../../ir/toolCall'
import type { AgentsRequest } from '../../../ir/tools/agents'
import type { ClaudeToolRow } from './toolCommon'
import { pickString } from '~/lib/jsonPick'
import { proseResult } from '../../../ir/toolCall'
import { CLAUDE_TOOL_NAMES } from '../toolNames'
import { claudeFailedResult } from './failure'

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
  // Trimmed HERE, so every caller sees the same string. The renderer treated a
  // whitespace-only listing as absent and handed the row to the catch-all, while
  // the toolbar read the untrimmed value as non-empty and offered a Copy button
  // that yielded spaces -- the exact disagreement the one-home rule above exists
  // to prevent.
  return (pickString(toolUseResult, 'listing', '') || resultContent).trim()
}

/**
 * The agents pair: the roster question, and the listing the tool answered with.
 *
 * The failure rung leads, and this kind needs it most: the listing renders as
 * MARKDOWN, so a reason that holds a `#` or a `*` drew as a heading or as emphasis.
 */
export function claudeAgentsPayload(request: AgentsRequest, args: ClaudeToolRow, result: ClaudeToolRow | undefined): ToolCallPayload<'agents'> {
  // The roster question, as the row's own header word. It reads the REQUEST rather than
  // the arguments a second time, so the header and the body cannot state two different
  // filters. `TeamCreate` and `TeamDelete` share this kind and name a TEAM rather than a
  // roster filter, so without that branch both drew the header "List agents" and the
  // team's own name appeared nowhere on the row.
  const teamName = request.team?.name
  const filters = [request.channel && `channel: ${request.channel}`, request.query && `matching: ${request.query}`].filter(Boolean).join(' · ')
  const teamAction = args.toolName === CLAUDE_TOOL_NAMES.TEAM_DELETE ? 'Delete' : 'Create'
  const title = teamName
    ? `${teamAction} team ${teamName}`
    : filters ? `List agents (${filters})` : 'List agents'
  if (!result)
    return { kind: 'agents', request, title }
  const failure = claudeFailedResult(result)
  if (failure)
    return { kind: 'agents', request, title, result: failure }
  const listing = claudeListAgentsListing(result.toolUseResult, result.resultContent)
  return { kind: 'agents', request, title, result: proseResult(listing, 'markdown') }
}
