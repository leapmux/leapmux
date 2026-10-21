import { COPILOT_METHOD } from '~/generated/contracts/copilot-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'

export const COPILOT_AUTO_SWITCH_RATE_LIMIT_CODE = 'user_model_rate_limited'

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
  if (!isObject(parsed) || pickString(parsed, 'method') !== COPILOT_METHOD.SessionEvent)
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

/** The data of one event of the given type, or null for every other row. */
export function copilotEventData(parsed: unknown, type: string): Record<string, unknown> | null {
  const event = copilotEvent(parsed)
  return event && event.type === type ? event.data : null
}

/** The kind of a native system notification. */
export const COPILOT_SYSTEM_NOTIFICATION_KIND = {
  AgentCompleted: 'agent_completed',
  AgentIdle: 'agent_idle',
  ShellCompleted: 'shell_completed',
  ShellDetachedCompleted: 'shell_detached_completed',
  InstructionDiscovered: 'instruction_discovered',
  FactoryCompleted: 'factory_completed',
} as const
