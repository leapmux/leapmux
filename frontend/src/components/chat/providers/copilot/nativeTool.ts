import { COPILOT_EVENT, COPILOT_SUPPLEMENT } from '~/generated/contracts/copilot-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'

/** Match native events to this ACP tool call before reading their fields. */
export function copilotNativeTool(tool: Record<string, unknown>, supplemental: Record<string, unknown> | undefined) {
  const events = supplemental?.[COPILOT_SUPPLEMENT.NativeEvents]
  const id = pickString(tool, 'toolCallId')
  if (!id || !Array.isArray(events))
    return undefined
  const matching = events.filter(isObject).filter(event => pickString(pickObject(event, 'data'), 'toolCallId') === id)
  const request = pickObject(matching.find(event => event.type === COPILOT_EVENT.ToolStarted), 'data')
  const name = pickString(request, 'toolName')
  if (!name)
    return undefined
  const completed = matching.find(event => event.type === COPILOT_EVENT.ToolCompleted)
  const started = matching.find(event => event.type === COPILOT_EVENT.SubagentStarted)
  const finished = matching.find(event => event.type === COPILOT_EVENT.SubagentCompleted || event.type === COPILOT_EVENT.SubagentFailed)
  return { name, request, completed, started, finished }
}
