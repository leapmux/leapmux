import { CLINE_EVENT_FIELD } from '~/generated/contracts/cline-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'

/**
 * The shape of the rows the Cline worker persists.
 *
 * Every row of a Cline transcript is one of Cline's own hub event envelopes, as the
 * daemon sent it or as the worker wrote it in the same shape:
 *
 *   {"version":"v1","event":"tool.started","sessionId":"...",
 *    "payload":{"toolCallId":"call_1","toolName":"read_files","input":{...}}}
 *
 * The event name and the payload field are the contract's `CLINE_EVENT_FIELD`, which
 * the worker writes with. The payload fields below are the ones only the browser reads
 * by name; the worker reads the same fields through its own struct tags.
 */

/** The payload fields of Cline's events that the browser reads. */
export const CLINE_FIELD = {
  ToolCallId: 'toolCallId',
  ToolName: 'toolName',
  Input: 'input',
  Output: 'output',
  Error: 'error',
  Text: 'text',
  Reasoning: 'reasoning',
  Media: 'media',
  Reason: 'reason',
  Result: 'result',
  Message: 'message',
  Metadata: 'metadata',
  Agent: 'agent',
} as const

/** One Cline event envelope: its name, and its payload. */
export interface ClineEnvelope {
  event: string
  payload: Record<string, unknown>
}

/** The envelope one row holds, or null for a row that is not a Cline event. */
export function clineEnvelope(message: unknown): ClineEnvelope | null {
  if (!isObject(message))
    return null
  const event = pickString(message, CLINE_EVENT_FIELD.Event)
  if (!event)
    return null
  return { event, payload: pickObject(message, CLINE_EVENT_FIELD.Payload) ?? {} }
}

/** The payload of one row when it is the event `event`, or null. */
export function clinePayload(message: unknown, event: string): Record<string, unknown> | null {
  const envelope = clineEnvelope(message)
  return envelope && envelope.event === event ? envelope.payload : null
}

/**
 * The words Cline closes the error of a refused tool call with
 * (`TOOL_REJECTION_SUFFIX` in `sdk/packages/shared/src/llms/tools.ts`). They tell the
 * model that the reader refused the call and that nothing failed, and they are how the
 * row tells a refusal from a failure.
 */
export const CLINE_REJECTION_SUFFIX = 'NOT a tool or system failure. Clarify with user before proceeding.'
