import type { ResolvedMessageContent } from '../../rowExtractionTypes'
import type { ToolSpanRole, ToolSpanSide } from '~/lib/messageSpan'
import { COMMAND_CODE_EVENT } from '~/generated/contracts/commandcode-protocol'
import { pickString } from '~/lib/jsonPick'
import { commandCodeEvent } from './protocol'

export const COMMAND_CODE_TOOL_RESULTS: ReadonlySet<string> = new Set([
  COMMAND_CODE_EVENT.ToolCompleted,
  COMMAND_CODE_EVENT.ToolErrored,
  COMMAND_CODE_EVENT.ToolDenied,
  COMMAND_CODE_EVENT.ToolHookBlocked,
])

export function commandCodeSpanRole(parsed: ResolvedMessageContent): ToolSpanRole {
  const event = commandCodeEvent(parsed.parentObject)
  if (!event || !pickString(event, 'toolCallId'))
    return 'other'
  const type = pickString(event, 'type')
  return type === COMMAND_CODE_EVENT.ToolQueued ? 'request' : COMMAND_CODE_TOOL_RESULTS.has(type) ? 'result' : 'other'
}

export function commandCodeRelatedMessages(parsed: ResolvedMessageContent): readonly ToolSpanSide[] {
  return commandCodeSpanRole(parsed) === 'result' ? ['request'] : []
}
