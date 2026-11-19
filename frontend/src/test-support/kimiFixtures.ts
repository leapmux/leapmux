import { KIMI_CONVERSATION_RECORD_KIND, KIMI_FRAME, KIMI_INTERACTION_KIND } from '~/generated/contracts/kimi-protocol'

const retiredTranscriptOps = 'transcript.ops'

/** A retired transcript envelope for rejection tests. */
function kimiTranscriptFrame(operation: Record<string, unknown>, agentId: string, sessionId: string) {
  return {
    type: retiredTranscriptOps,
    session_id: sessionId,
    payload: {
      type: retiredTranscriptOps,
      agent_id: agentId,
      seq: 1,
      ops: [operation],
    },
  }
}

/** A retired tool frame with stable turn, step and call identities. */
export function kimiTranscriptToolFrame(frame: Record<string, unknown>, agentId = 'main', sessionId = 'session_1') {
  const toolCallId = typeof frame.toolCallId === 'string'
    ? frame.toolCallId
    : 'call_1'
  return kimiTranscriptFrame({
    op: 'frame.upsert',
    turnId: 't0',
    stepId: 't0.1',
    frame: {
      kind: 'tool',
      frameId: `t0.1.${toolCallId}`,
      toolCallId,
      state: 'running',
      ...frame,
    },
  }, agentId, sessionId)
}

/** A retired turn header with complete ownership. */
export function kimiTranscriptTurnFrame(turn: Record<string, unknown>, agentId = 'main', sessionId = 'session_1') {
  return kimiTranscriptFrame({
    op: 'turn.upsert',
    turn: {
      kind: 'turn',
      turnId: 't0',
      ordinal: 0,
      state: 'running',
      origin: { kind: 'user' },
      ...turn,
    },
  }, agentId, sessionId)
}

/** A retired marker with its original diagnostic payload. */
export function kimiTranscriptMarkerFrame(marker: string, payload: Record<string, unknown> = {}, agentId = 'main', sessionId = 'session_1') {
  return kimiTranscriptFrame({
    op: 'marker.upsert',
    item: {
      kind: 'marker',
      markerId: 'live-m1',
      marker,
      payload,
      at: '2026-11-15T00:00:00Z',
    },
  }, agentId, sessionId)
}

/** One event row of the main agent, or of the given agent. */
export function kimiFrame(type: string, fields: Record<string, unknown> = {}, agentId = 'main'): Record<string, unknown> {
  return { type, agentId, ...fields }
}

type KimiControlFixture = Record<string, unknown> & { request: Record<string, unknown> }

/** One pending approval, as the control surface receives it. */
export function kimiApprovalRequest(toolName: string, display: Record<string, unknown>, fields: Record<string, unknown> = {}): KimiControlFixture {
  const { agent_id = 'main', ...requestFields } = fields
  return {
    type: KIMI_FRAME.Interaction,
    kind: KIMI_INTERACTION_KIND.Approval,
    status: 'pending',
    interaction_id: 'approval_1',
    session_id: 'session_1',
    agent_id,
    tool_call_id: 'call_1',
    request: {
      tool_name: toolName,
      action: `Running: ${toolName}`,
      tool_input_display: display,
      ...requestFields,
    },
  }
}

/** One pending question request, as the control surface receives it. */
export function kimiQuestionRequest(questions: Record<string, unknown>[]): KimiControlFixture {
  return {
    type: KIMI_FRAME.Interaction,
    kind: KIMI_INTERACTION_KIND.Question,
    status: 'pending',
    interaction_id: 'question_1',
    session_id: 'session_1',
    agent_id: 'main',
    tool_call_id: 'call_ask',
    request: { questions },
  }
}

interface KimiFixtureOwner {
  sessionId?: string
  agentId?: string
  turnId?: string
}

const KIMI_FIXTURE_TIME = '2026-11-18T00:00:00Z'

/** One retained public conversation item with an explicit Worker role. */
export function kimiConversationItemRecord(item: Record<string, unknown>, role: 'item' | 'request' | 'result' = 'item', owner: KimiFixtureOwner = {}) {
  return {
    type: KIMI_CONVERSATION_RECORD_KIND.Item,
    session_id: owner.sessionId ?? 'session_1',
    agent_id: owner.agentId ?? 'main',
    turn_id: owner.turnId ?? 't0',
    role,
    event_created_at: KIMI_FIXTURE_TIME,
    item: {
      item_id: 'item-1',
      status: 'running',
      created_at: KIMI_FIXTURE_TIME,
      ...item,
    },
  }
}

/** One retained public tool request or result. A local result can still carry a running native item. */
export function kimiConversationToolRecord(role: 'request' | 'result', tool: Record<string, unknown>, owner?: KimiFixtureOwner) {
  return kimiConversationItemRecord({
    type: 'tool_call',
    started_at: KIMI_FIXTURE_TIME,
    ...tool,
  }, role, owner)
}

/** One retained public turn header. The caller specifies its native outcome. */
export function kimiConversationTurnRecord(turn: Record<string, unknown>, owner: KimiFixtureOwner = {}) {
  const turnId = owner.turnId ?? 't0'
  return {
    type: KIMI_CONVERSATION_RECORD_KIND.Turn,
    session_id: owner.sessionId ?? 'session_1',
    agent_id: owner.agentId ?? 'main',
    turn_id: turnId,
    event_created_at: KIMI_FIXTURE_TIME,
    turn: {
      turn_id: turnId,
      kind: 'agent',
      ordinal: 0,
      status: 'running',
      started_at: KIMI_FIXTURE_TIME,
      steps: [],
      ...turn,
    },
  }
}
