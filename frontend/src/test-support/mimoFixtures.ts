import type { ClassificationInput } from '~/components/chat/providers/registry'
import { input } from '~/components/chat/providers/testUtils'
import { MIMO_EVENT, MIMO_PART_TYPE, MIMO_TOOL_STATUS } from '~/generated/contracts/mimo-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'

/*
 * MiMo Code's persisted rows, for the plugin's own tests and the cross-provider
 * parity tests.
 *
 * MiMo speaks its own native protocol, and the worker keeps each event that it draws
 * verbatim as `{type, properties}`. Every test that feeds MiMo its own wire shape
 * builds it here, so one change to that shape reaches all of them.
 */

/** The session every MiMo test frame belongs to. */
export const TEST_SESSION = 'ses_test'

/** One stored MiMo event, as the worker keeps it. */
export function mimoFrame(type: string, properties: Record<string, unknown>): Record<string, unknown> {
  return { type, properties }
}

/** The state of one tool part. */
export interface ToolStateFields {
  status?: string
  input?: Record<string, unknown>
  output?: string
  error?: string
  title?: string
  metadata?: Record<string, unknown>
  attachments?: Record<string, unknown>[]
}

/** A stored `message.part.updated` event of one tool part. */
export function toolFrame(tool: string, state: ToolStateFields, callID = 'call-1'): Record<string, unknown> {
  return mimoFrame(MIMO_EVENT.MessagePartUpdated, {
    sessionID: TEST_SESSION,
    part: {
      id: `prt_${callID}`,
      messageID: 'msg_1',
      sessionID: TEST_SESSION,
      type: MIMO_PART_TYPE.Tool,
      tool,
      callID,
      state: { status: MIMO_TOOL_STATUS.Completed, input: {}, ...state },
    },
  })
}

/** The opening frame of a call: the first running update, which states the input. */
export function openingFrame(tool: string, callInput: Record<string, unknown>, callID = 'call-1'): Record<string, unknown> {
  return toolFrame(tool, { status: MIMO_TOOL_STATUS.Running, input: callInput }, callID)
}

/** A frame as the parsed message the span store resolves. */
export function parsedFrame(frame: Record<string, unknown>): ClassificationInput {
  return input(frame, undefined, AgentProvider.MIMO_CODE)
}

/** A `session.status` frame. */
export function statusFrame(statusType: string, extra: Record<string, unknown> = {}): Record<string, unknown> {
  return mimoFrame(MIMO_EVENT.SessionStatus, { sessionID: TEST_SESSION, status: { type: statusType, ...extra } })
}

/** A `session.error` frame. */
export function errorFrame(name: string, message: string): Record<string, unknown> {
  return mimoFrame(MIMO_EVENT.SessionError, { sessionID: TEST_SESSION, error: { name, data: { message } } })
}

/** A compaction part: the start carries no projection, and the end carries the summary. */
export function compactionFrame(ended: boolean, auto = false): Record<string, unknown> {
  return mimoFrame(MIMO_EVENT.MessagePartUpdated, {
    sessionID: TEST_SESSION,
    part: {
      id: 'prt_compaction',
      messageID: 'msg_u',
      sessionID: TEST_SESSION,
      type: MIMO_PART_TYPE.Compaction,
      auto,
      ...(ended ? { projection: { summary: 'Earlier work, summarized.' } } : {}),
    },
  })
}
