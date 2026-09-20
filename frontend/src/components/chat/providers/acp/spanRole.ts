import type { ACPToolCallAdapter } from './extractors/toolCall'
import type { ResolvedMessageContent } from '~/components/chat/rowExtractionTypes'
import type { ToolSpanRole, ToolSpanSide } from '~/lib/messageSpan'
import { acpToolCallNeedsResult, acpToolFinished } from './extractors/toolCall'
import { ACP_SESSION_UPDATE } from './updateVocabulary'

/** Read the tool-span role from one resolved Agent Client Protocol frame. */
export function acpSpanRole(parsed: ResolvedMessageContent): ToolSpanRole {
  const tool = parsed.parentObject
  if (tool?.sessionUpdate === ACP_SESSION_UPDATE.TOOL_CALL)
    return acpToolFinished(tool, parsed.completion) ? 'result' : 'request'
  if (tool?.sessionUpdate === ACP_SESSION_UPDATE.TOOL_CALL_UPDATE && acpToolFinished(tool, parsed.completion))
    return 'result'
  return 'other'
}

/** Bind one provider's tool adapter to the shared related-message reader. */
export function createACPRelatedMessagesReader(adapter?: ACPToolCallAdapter): (parsed: ResolvedMessageContent) => readonly ToolSpanSide[] {
  return (parsed) => {
    const tool = parsed.parentObject
    if (!tool)
      return []
    if (acpToolFinished(tool, parsed.completion))
      return ['request']
    return (tool.sessionUpdate === ACP_SESSION_UPDATE.TOOL_CALL && acpToolCallNeedsResult(tool, adapter, parsed.supplementalContent))
      ? ['result']
      : []
  }
}
