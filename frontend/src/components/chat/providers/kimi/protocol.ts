import { KIMI_EVENT } from '~/generated/contracts/kimi-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'

/**
 * Kimi Code vocabulary that only the frontend reads, and the one reader of a
 * persisted event row.
 *
 * The worker persists every event of the kap-server VERBATIM: a row is the event
 * payload itself, whose `type` is the event type and whose `agentId` states the agent
 * of the session that produced it. A row the worker builds itself -- the assembled
 * text and thinking, a user message, LeapMux's own notices -- carries no Kimi event
 * type, which is how every reader here tells the two apart.
 *
 * Vocabulary that Go and TypeScript both read lives in contracts/kimi-protocol.json.
 */

/** Every event type the contract lists. A row of any other type is not a Kimi event. */
const KIMI_EVENT_TYPES: ReadonlySet<string> = new Set<string>(Object.values(KIMI_EVENT))

/** The agent id the main agent of a session carries. */
export const KIMI_MAIN_AGENT = 'main'

/** One persisted Kimi Code event. */
export interface KimiEventRow {
  type: string
  /** The agent that produced the event: `main`, or a subagent's `agent-N`. */
  agentId: string
  /** The whole event payload, which is the row itself. */
  data: Record<string, unknown>
}

/** Read one persisted row as a Kimi event, or null for a row that is not one. */
export function kimiEvent(parsed: unknown): KimiEventRow | null {
  if (!isObject(parsed))
    return null
  const type = pickString(parsed, 'type')
  if (!KIMI_EVENT_TYPES.has(type))
    return null
  return { type, agentId: pickString(parsed, 'agentId') || KIMI_MAIN_AGENT, data: parsed }
}

/** The payload of one event of the given type, or null for every other row. */
export function kimiEventData(parsed: unknown, type: string): Record<string, unknown> | null {
  const event = kimiEvent(parsed)
  return event && event.type === type ? event.data : null
}

/**
 * The `display` object of a tool call, or of an approval's `tool_input_display`.
 *
 * The server words a call for its own UI here: the command and its working directory,
 * the file operation, the plan of a plan review. It is the richest statement of a call
 * the wire carries, and the result frame repeats none of it.
 */
export function kimiDisplay(data: Record<string, unknown> | null | undefined, key: 'display' | 'tool_input_display' = 'display'): Record<string, unknown> | undefined {
  return pickObject(data, key, undefined)
}

/** The `system_trigger` name a goal's continuation turn carries. */
export const KIMI_GOAL_CONTINUATION = 'goal_continuation'

/** Native fields for tool result pointers. */
export const KIMI_OUTPUT_FILE_RESULT = {
  Type: 'type',
  AgentID: 'agentId',
  TurnID: 'turnId',
  ToolCallID: 'toolCallId',
  Output: 'output',
} as const

/** Native fields for tool result pointers. */
export const KIMI_OUTPUT_FILE_CONTENT = {
  Type: 'type',
  Text: 'text',
} as const

/** Native fields for tool result pointers. */
export const KIMI_OUTPUT_FILE_POINTER = {
  ToolName: 'tool_name',
  ToolCallID: 'tool_call_id',
  CharacterCount: 'output_size_chars',
  ByteCount: 'output_size_bytes',
  Path: 'output_path',
} as const

/** Native fields for tool result pointers. */
export const KIMI_OUTPUT_FILE_LIMIT = {
  InlineCharacters: 50000,
} as const

/** Native fields for output pointer notices. */
export const KIMI_OUTPUT_FILE_HEADER = {
  Complete: `Tool output exceeded ${KIMI_OUTPUT_FILE_LIMIT.InlineCharacters} characters; the full output was saved to a file.`,
  CompleteText: `Tool output exceeded ${KIMI_OUTPUT_FILE_LIMIT.InlineCharacters} characters; the full text output was saved to a file (media parts stay attached to this result).`,
  NextStep: 'next_step: Use Read with output_path to page through the saved output, or Grep to search it.',
  PerLineMarker: '[Per-line truncation occurred;',
  PerLineComplete: '[Per-line truncation occurred; the complete output was saved to a file.',
  PerLineCompleteText: '[Per-line truncation occurred; the complete text output was saved to a file (media parts stay attached to this result).',
  PerLineNextStep: 'next_step: Use Read with output_path to page through the saved output, or Grep to search it.]',
} as const

/** Native fields for tool result pointers. */
export const KIMI_OUTPUT_FILE_CONTENT_KIND = {
  Text: 'text',
} as const
