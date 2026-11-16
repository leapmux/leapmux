import { KIMI_EVENT, KIMI_TRANSCRIPT_FRAME, KIMI_TRANSCRIPT_NODE, KIMI_TRANSCRIPT_OPERATION, KIMI_TRANSCRIPT_ROLE, KIMI_TRANSCRIPT_STEP, KIMI_TRANSCRIPT_TOOL } from '~/generated/contracts/kimi-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'

export interface KimiTranscriptRow {
  sessionId: string
  agentId: string
  operation: Record<string, unknown>
}

/** Read the single native operation retained in one Worker row. */
export function kimiTranscriptRow(parsed: unknown): KimiTranscriptRow | null {
  if (!isObject(parsed) || pickString(parsed, 'type') !== KIMI_EVENT.TranscriptOps)
    return null
  const sessionId = pickString(parsed, 'session_id')
  const payload = pickObject(parsed, 'payload')
  const agentId = pickString(payload, 'agent_id')
  if (!sessionId || !agentId || pickString(payload, 'type') !== KIMI_EVENT.TranscriptOps)
    return null
  if (payload?.seq !== undefined && (!Number.isSafeInteger(payload.seq) || typeof payload.seq !== 'number' || payload.seq < 0))
    return null
  const operations = payload?.ops
  if (!Array.isArray(operations) || operations.length !== 1 || !isObject(operations[0]))
    return null
  return { sessionId, agentId, operation: operations[0] }
}

/** Read a native exported turn header without changing its fields. */
export function kimiTranscriptTurn(parsed: unknown): Record<string, unknown> | null {
  const row = kimiTranscriptRow(parsed)
  if (!row || pickString(row.operation, 'op') !== KIMI_TRANSCRIPT_OPERATION.TurnUpsert)
    return null
  const turn = pickObject(row.operation, 'turn')
  return turn && pickString(turn, 'kind') === KIMI_TRANSCRIPT_NODE.Turn && pickString(turn, 'turnId')
    ? turn
    : null
}

/** Read a task notification projected as user-role text in a native step. */
export function kimiTranscriptTaskText(parsed: unknown): string | null {
  const row = kimiTranscriptRow(parsed)
  if (!row || pickString(row.operation, 'op') !== KIMI_TRANSCRIPT_OPERATION.FrameUpsert)
    return null
  const frame = pickObject(row.operation, 'frame')
  return frame && pickString(frame, 'kind') === KIMI_TRANSCRIPT_FRAME.Text && pickString(frame, 'role') === KIMI_TRANSCRIPT_ROLE.User && pickString(frame, 'taskId')
    ? pickString(frame, 'text')
    : null
}

/** Read one native marker while retaining its payload. */
export function kimiTranscriptMarker(parsed: unknown): Record<string, unknown> | null {
  const row = kimiTranscriptRow(parsed)
  if (!row || pickString(row.operation, 'op') !== KIMI_TRANSCRIPT_OPERATION.MarkerUpsert)
    return null
  const item = pickObject(row.operation, 'item')
  return item && pickString(item, 'kind') === KIMI_TRANSCRIPT_NODE.Marker && pickString(item, 'markerId')
    ? item
    : null
}

/** Read retry facts from their complete native step operation. */
export function kimiTranscriptRetry(parsed: unknown): Record<string, unknown> | null {
  const row = kimiTranscriptRow(parsed)
  if (!row || pickString(row.operation, 'op') !== KIMI_TRANSCRIPT_OPERATION.StepUpsert || !pickString(row.operation, 'turnId'))
    return null
  const step = pickObject(row.operation, 'step')
  const retry = pickObject(step, 'retry')
  if (step?.kind !== KIMI_TRANSCRIPT_NODE.Step || step.state !== KIMI_TRANSCRIPT_STEP.Running || !pickString(step, 'stepId') || !retry)
    return null
  const { failedAttempt, nextAttempt, maxAttempts, delayMs } = retry
  if (typeof failedAttempt !== 'number' || !Number.isSafeInteger(failedAttempt) || failedAttempt < 1
    || typeof nextAttempt !== 'number' || !Number.isSafeInteger(nextAttempt) || nextAttempt <= failedAttempt
    || typeof maxAttempts !== 'number' || !Number.isSafeInteger(maxAttempts) || maxAttempts < nextAttempt
    || typeof delayMs !== 'number' || !Number.isFinite(delayMs) || delayMs < 0) {
    return null
  }
  return retry
}

export interface KimiTranscriptToolRow extends KimiTranscriptRow {
  turnId: string
  stepId: string
  frameId: string
  toolCallId: string
  name: string
  state: string
  frame: Record<string, unknown>
}

/** Read a complete native tool frame and its ownership. */
export function kimiTranscriptTool(parsed: unknown): KimiTranscriptToolRow | null {
  const row = kimiTranscriptRow(parsed)
  if (!row || pickString(row.operation, 'op') !== KIMI_TRANSCRIPT_OPERATION.FrameUpsert)
    return null
  const frame = pickObject(row.operation, 'frame')
  const turnId = pickString(row.operation, 'turnId')
  const stepId = pickString(row.operation, 'stepId')
  const frameId = pickString(frame, 'frameId')
  const toolCallId = pickString(frame, 'toolCallId')
  const state = pickString(frame, 'state')
  if (!frame || pickString(frame, 'kind') !== KIMI_TRANSCRIPT_FRAME.Tool || !turnId || !stepId || !frameId || !toolCallId || typeof frame.name !== 'string')
    return null
  if (state !== KIMI_TRANSCRIPT_TOOL.Running && state !== KIMI_TRANSCRIPT_TOOL.Done && state !== KIMI_TRANSCRIPT_TOOL.Error)
    return null
  return {
    ...row,
    turnId,
    stepId,
    frameId,
    toolCallId,
    name: frame.name,
    state,
    frame,
  }
}

/** Require every native owner and identity before pairing two stored frames. */
export function kimiTranscriptToolsMatch(left: KimiTranscriptToolRow, right: KimiTranscriptToolRow | null): boolean {
  return right !== null
    && left.sessionId === right.sessionId
    && left.agentId === right.agentId
    && left.turnId === right.turnId
    && left.stepId === right.stepId
    && left.frameId === right.frameId
    && left.toolCallId === right.toolCallId
}
