import { KIMI_EVENT, KIMI_FRAME, KIMI_INTERACTION_KIND } from '~/generated/contracts/kimi-protocol'

/** One native transcript operation with its session and agent ownership. */
export function kimiTranscriptFrame(operation: Record<string, unknown>, agentId = 'main', sessionId = 'session_1') {
  return {
    type: KIMI_EVENT.TranscriptOps,
    session_id: sessionId,
    payload: {
      type: KIMI_EVENT.TranscriptOps,
      agent_id: agentId,
      seq: 1,
      ops: [operation],
    },
  }
}

/** A native tool frame with stable turn, step and call identities. */
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

/** One exported native turn header with complete ownership. */
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

/** One native marker with its original diagnostic payload. */
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

/**
 * Build persisted warning and diagnostic payloads. Their transport frame supplies
 * the session separately. Native tools use complete transcript operations above.
 * Control interactions use snake-case identity fields and a nested request.
 */

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
