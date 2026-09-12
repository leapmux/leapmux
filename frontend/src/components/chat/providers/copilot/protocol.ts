import { COPILOT_TOOL } from '~/generated/contracts/copilot-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'

/** The JSON-RPC method that carries every Copilot session event. */
export const COPILOT_EVENT_METHOD = 'session.event'

/**
 * One native session event, as a persisted row holds it.
 *
 * A row stores the WHOLE frame the runtime sent, so the event sits two levels down.
 * Every reader goes through {@link copilotEvent}, so the unwrapping happens once.
 */
export interface CopilotEventEnvelope {
  /** The event's own identifier. */
  id: string
  type: string
  /** The subagent that emitted the event. Empty for the session's own agent. */
  agentId: string
  data: Record<string, unknown>
}

/** Unwrap a persisted Copilot row. Null for any other row, so callers use it as a guard. */
export function copilotEvent(parsed: unknown): CopilotEventEnvelope | null {
  if (!isObject(parsed) || pickString(parsed, 'method') !== COPILOT_EVENT_METHOD)
    return null
  const event = pickObject(pickObject(parsed, 'params'), 'event')
  const type = pickString(event, 'type')
  if (!event || !type)
    return null
  return {
    id: pickString(event, 'id'),
    type,
    agentId: pickString(event, 'agentId'),
    data: pickObject(event, 'data') ?? {},
  }
}

/** The data of one event of the named type, or null for every other row. */
export function copilotEventData(parsed: unknown, type: string): Record<string, unknown> | null {
  const event = copilotEvent(parsed)
  return event && event.type === type ? event.data : null
}

/**
 * The shared presentation kind for one native tool.
 *
 * The names are Copilot's own, read from `session.tools.getBuiltinDescriptors` on the
 * installed runtime. A tool this table does not list -- a Model Context Protocol tool,
 * an extension tool, a tool a later release adds -- takes the generic kind, which
 * renders its arguments and its content without claiming a shape it does not have.
 */
const COPILOT_TOOL_KINDS: Record<string, string> = {
  [COPILOT_TOOL.View]: 'read',
  [COPILOT_TOOL.Create]: 'write',
  [COPILOT_TOOL.Edit]: 'edit',
  [COPILOT_TOOL.StrReplaceEditor]: 'edit',
  [COPILOT_TOOL.ApplyPatch]: 'edit',
  [COPILOT_TOOL.Bash]: 'execute',
  [COPILOT_TOOL.ReadBash]: 'execute',
  [COPILOT_TOOL.ListBash]: 'execute',
  [COPILOT_TOOL.StopBash]: 'execute',
  [COPILOT_TOOL.Glob]: 'glob',
  [COPILOT_TOOL.Grep]: 'grep',
  [COPILOT_TOOL.WebFetch]: 'fetch',
  [COPILOT_TOOL.Task]: 'agent',
  [COPILOT_TOOL.UpdateTodo]: 'todo',
}

export function copilotToolKind(toolName: string): string {
  return COPILOT_TOOL_KINDS[toolName] ?? 'other'
}
