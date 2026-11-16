import type { ResolvedMessageContent } from '~/components/chat/rowExtractionTypes'
import type { ToolSpanRole } from '~/lib/messageSpan'
import { acpToolFinished } from './extractors/toolCall'
import { ACP_SESSION_UPDATE } from './updateVocabulary'

/** Read the tool-span role from one resolved Agent Client Protocol frame. */
export function acpSpanRole(parsed: ResolvedMessageContent): ToolSpanRole {
  const tool = parsed.parentObject
  if (tool?.sessionUpdate === ACP_SESSION_UPDATE.TOOL_CALL) {
    return acpToolFinished(tool, parsed.completion)
      ? 'result'
      : 'request'
  }
  if (tool?.sessionUpdate === ACP_SESSION_UPDATE.TOOL_CALL_UPDATE && acpToolFinished(tool, parsed.completion))
    return 'result'
  return 'other'
}

/**
 * Whether the row of one Agent Client Protocol frame draws from its span's request.
 *
 * A FINISHED frame does: the update that closes a call often states no kind, no title
 * and no arguments, and its row merges them from the request (see `acpExtractRow`).
 * Every other frame draws from its own fields alone. The frame's own status decides,
 * as it decides the row's role.
 */
export function acpNeedsRequest(parsed: ResolvedMessageContent): boolean {
  const tool = parsed.parentObject
  return tool !== undefined
    && (tool.sessionUpdate === ACP_SESSION_UPDATE.TOOL_CALL || tool.sessionUpdate === ACP_SESSION_UPDATE.TOOL_CALL_UPDATE)
    && acpToolFinished(tool, parsed.completion)
}
