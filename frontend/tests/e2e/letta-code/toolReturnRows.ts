import type { AgentChatMessage } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { AgentEvent } from '../../../src/generated/proto/leapmux/v1/workspace_pb'
import { decompressContentToString } from '../../../src/lib/decompress'

/** The Letta message type of the message that carries the result of a tool call. */
const TOOL_RETURN_MESSAGE = 'tool_return_message'

/**
 * Return the raw content of a Letta tool result row, or undefined for any other row.
 *
 * A row holds the message that Letta sent, so the content of a tool result row holds its message type. A row whose
 * content does not decode is not a tool result row.
 */
export function toolReturnRow(message: Pick<AgentChatMessage, 'content' | 'contentCompression'>): string | undefined {
  const raw = decompressContentToString(message.content, message.contentCompression)
  return raw?.includes(TOOL_RETURN_MESSAGE) ? raw : undefined
}

/**
 * Return the raw content of a LIVE Letta tool result row that one watched event carries, or undefined for any other
 * event. A replayed row is not live: a watch replays the latest rows of the agent when it subscribes.
 */
export function liveToolReturnRow(event: AgentEvent): string | undefined {
  if (event.replay || event.event.case !== 'agentMessage')
    return undefined
  return toolReturnRow(event.event.value)
}
