import type { KimiConversationRecordRole } from '~/generated/contracts/kimi-protocol'
import { KIMI_CONVERSATION_FIELD as FIELD, KIMI_CONVERSATION_ITEM_KIND as ITEM, KIMI_CONVERSATION_END_REASON, KIMI_CONVERSATION_TURN_KIND, KIMI_FRAME, KIMI_NOTICE_LEVEL, KIMI_CONVERSATION_ORIGIN as ORIGIN, KIMI_CONVERSATION_OUTCOME as OUTCOME, KIMI_CONVERSATION_RECORD_KIND as RECORD, KIMI_CONVERSATION_RECORD_ROLE as ROLE, KIMI_CONVERSATION_STATUS as STATUS } from '~/generated/contracts/kimi-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { kimiConversationUserFields } from './conversationValidation'

interface KimiConversationOwner {
  record: Record<string, unknown>
  sessionId: string
  agentId: string
}

export type KimiConversationRow
  = | KimiConversationOwner & { kind: 'item', turnId: string, role: KimiConversationRecordRole, item: Record<string, unknown>, turnContext?: Record<string, unknown> }
    | KimiConversationOwner & { kind: 'turn', turnId: string, turn: Record<string, unknown> }
    | KimiConversationOwner & { kind: 'notice', notice: Record<string, unknown> }
    | KimiConversationOwner & { kind: 'failure', failure: Record<string, unknown> }
    | KimiConversationOwner & { kind: 'retry', turnId: string, stepIndex: number, retry: Record<string, unknown> }

function timestamp(value: unknown): boolean {
  return typeof value === 'string' && value.includes('T') && Number.isFinite(Date.parse(value))
}

function status(value: unknown): boolean {
  return value === STATUS.Running || value === STATUS.Ended
}

function outcome(value: unknown): boolean {
  return value === undefined || value === OUTCOME.Completed || value === OUTCOME.Interrupted || value === OUTCOME.Failed
}

function positiveInteger(value: unknown): boolean {
  return typeof value === 'number' && Number.isSafeInteger(value) && value > 0
}

function itemFields(item: Record<string, unknown>): boolean {
  const promptIDs = item[FIELD.PromptIDs]
  if (!pickString(item, FIELD.ItemID) || !timestamp(item[FIELD.CreatedAt]) || !status(item[FIELD.Status]) || !outcome(item[FIELD.Outcome])
    || (item[FIELD.StepIndex] !== undefined && !positiveInteger(item[FIELD.StepIndex]))) {
    return false
  }
  switch (item[FIELD.Type]) {
    case ITEM.AssistantMessage:
      return typeof item[FIELD.Text] === 'string'
    case ITEM.Reasoning:
      return typeof item[FIELD.Text] === 'string' && timestamp(item[FIELD.StartedAt])
    case ITEM.ToolCall:
      return typeof item[FIELD.Name] === 'string' && timestamp(item[FIELD.StartedAt])
        && (item[FIELD.Output] === undefined || typeof item[FIELD.Output] === 'string')
    case ITEM.UserMessage:
      return typeof item[FIELD.Text] === 'string' && Array.isArray(promptIDs)
        && promptIDs.every(id => typeof id === 'string' && id !== '')
        && kimiConversationUserFields(item)
    case ITEM.Notification:
      return typeof item[FIELD.Title] === 'string' && typeof item[FIELD.Body] === 'string' && typeof item[FIELD.NotificationID] === 'string'
    case ITEM.System:
      return typeof item[FIELD.Kind] === 'string'
    default:
      return false
  }
}

/** Read the Worker's record while retaining the native public item fields. */
export function kimiConversationRow(value: unknown): KimiConversationRow | null {
  if (!isObject(value))
    return null
  const sessionId = pickString(value, FIELD.SessionID)
  const agentId = pickString(value, FIELD.AgentID)
  if (!sessionId || !agentId || (value[FIELD.EventCreatedAt] !== undefined && !timestamp(value[FIELD.EventCreatedAt])))
    return null
  const owner = { record: value, sessionId, agentId }
  const turnId = pickString(value, FIELD.TurnID)
  const outputTruncated = value[FIELD.OutputTruncated]
  if (outputTruncated !== undefined && (typeof outputTruncated !== 'boolean' || value[FIELD.Type] !== RECORD.Item || value[FIELD.Role] !== ROLE.Result))
    return null
  if (value[FIELD.Type] !== RECORD.Failure && value[FIELD.Failure] !== undefined)
    return null
  if (value[FIELD.Type] === RECORD.Retry) {
    const retry = pickObject(value, FIELD.Retry)
    const error = pickObject(retry, FIELD.Error)
    const delayMs = retry?.[FIELD.DelayMS]
    if (!turnId || !positiveInteger(value[FIELD.StepIndex]) || !retry || !positiveInteger(retry[FIELD.Attempt]) || !positiveInteger(retry[FIELD.NextAttempt])
      || !positiveInteger(retry[FIELD.MaxAttempts]) || Number(retry[FIELD.NextAttempt]) <= Number(retry[FIELD.Attempt]) || Number(retry[FIELD.MaxAttempts]) < Number(retry[FIELD.NextAttempt])
      || typeof delayMs !== 'number' || !Number.isFinite(delayMs) || delayMs < 0 || !error
      || typeof error[FIELD.Name] !== 'string' || typeof error[FIELD.Message] !== 'string' || value[FIELD.Item] !== undefined || value[FIELD.Turn] !== undefined || value[FIELD.Notice] !== undefined || value[FIELD.Role] !== undefined || value[FIELD.TurnContext] !== undefined) {
      return null
    }
    return {
      ...owner,
      kind: 'retry',
      turnId,
      stepIndex: Number(value[FIELD.StepIndex]),
      retry,
    }
  }
  if (value[FIELD.StepIndex] !== undefined || value[FIELD.Retry] !== undefined)
    return null
  if (value[FIELD.Type] !== RECORD.Item && value[FIELD.TurnContext] !== undefined)
    return null
  if (value[FIELD.Type] === RECORD.Failure) {
    const failure = pickObject(value, FIELD.Failure)
    if (!failure || failure[FIELD.Type] !== KIMI_FRAME.TurnsFailed
      || failure[FIELD.SessionID] !== sessionId || failure[FIELD.AgentID] !== agentId
      || failure[FIELD.EventCreatedAt] !== value[FIELD.EventCreatedAt] || !timestamp(value[FIELD.EventCreatedAt])
      || !Number.isSafeInteger(failure[FIELD.Code]) || typeof failure[FIELD.Msg] !== 'string'
      || value[FIELD.TurnID] !== undefined || value[FIELD.Role] !== undefined
      || value[FIELD.Item] !== undefined || value[FIELD.Turn] !== undefined || value[FIELD.Notice] !== undefined) {
      return null
    }
    return { ...owner, kind: 'failure', failure }
  }
  if (value[FIELD.Type] === RECORD.Item) {
    const item = pickObject(value, FIELD.Item)
    if (!turnId || !item || !itemFields(item) || value[FIELD.Turn] !== undefined || value[FIELD.Notice] !== undefined)
      return null
    if (outputTruncated !== undefined && (item[FIELD.Type] !== ITEM.ToolCall || typeof item[FIELD.Output] !== 'string'))
      return null
    const role = value[FIELD.Role]
    const turnContext = pickObject(value, FIELD.TurnContext)
    if (value[FIELD.TurnContext] !== undefined && (!turnContext || turnContext[FIELD.TurnID] !== turnId
      || (turnContext[FIELD.Kind] !== KIMI_CONVERSATION_TURN_KIND.Agent && turnContext[FIELD.Kind] !== KIMI_CONVERSATION_TURN_KIND.System)
      || !status(turnContext[FIELD.Status]) || !outcome(turnContext[FIELD.Outcome])
      || (turnContext[FIELD.EndReason] !== undefined && typeof turnContext[FIELD.EndReason] !== 'string') || role === ROLE.Request)) {
      return null
    }
    if (item[FIELD.Type] === ITEM.ToolCall) {
      if (role !== ROLE.Request && role !== ROLE.Result)
        return null
    }
    else if (role === ROLE.Origin) {
      const originKind = pickObject(item, FIELD.Origin)?.[FIELD.Kind]
      if (item[FIELD.Type] !== ITEM.UserMessage || item[FIELD.Status] !== STATUS.Ended || item[FIELD.Placement] !== 'opener'
        || originKind === ORIGIN.User || originKind === ORIGIN.System) {
        return null
      }
    }
    else if (role !== ROLE.Item) {
      return null
    }
    return {
      ...owner,
      kind: 'item',
      turnId,
      role,
      item,
      ...(turnContext
        ? { turnContext }
        : {}),
    }
  }
  if (value[FIELD.Type] === RECORD.Turn) {
    const turn = pickObject(value, FIELD.Turn)
    const ordinal = turn?.[FIELD.Ordinal]
    if (!turnId || !turn || turn[FIELD.TurnID] !== turnId || !status(turn[FIELD.Status]) || !outcome(turn[FIELD.Outcome])
      || typeof ordinal !== 'number' || !Number.isSafeInteger(ordinal) || ordinal < 0
      || !timestamp(turn[FIELD.StartedAt]) || !Array.isArray(turn[FIELD.Steps]) || typeof turn[FIELD.Kind] !== 'string'
      || value[FIELD.Role] !== undefined || value[FIELD.Item] !== undefined || value[FIELD.Notice] !== undefined) {
      return null
    }
    return { ...owner, kind: 'turn', turnId, turn }
  }
  if (value[FIELD.Type] === RECORD.Notice) {
    const notice = pickObject(value, FIELD.Notice)
    if (!notice || typeof notice[FIELD.Title] !== 'string' || !timestamp(value[FIELD.EventCreatedAt])
      || (notice[FIELD.Level] !== KIMI_NOTICE_LEVEL.Info && notice[FIELD.Level] !== KIMI_NOTICE_LEVEL.Warning && notice[FIELD.Level] !== KIMI_NOTICE_LEVEL.Error)
      || value[FIELD.TurnID] !== undefined || value[FIELD.Role] !== undefined || value[FIELD.Item] !== undefined || value[FIELD.Turn] !== undefined) {
      return null
    }
    return { ...owner, kind: 'notice', notice }
  }
  return null
}

/** Only an explicit native failure can turn hook markup into an operational notice. */
export function kimiTurnIsBlocked(turn: Record<string, unknown> | undefined): boolean {
  return turn?.[FIELD.Kind] === KIMI_CONVERSATION_TURN_KIND.Agent && turn[FIELD.Status] === STATUS.Ended
    && turn[FIELD.Outcome] === OUTCOME.Failed && turn[FIELD.EndReason] === KIMI_CONVERSATION_END_REASON.Blocked
}

/** Accept a rendering view only when it retains every known native ownership fact. */
export function kimiConversationAcceptsView(original: KimiConversationRow, incoming: KimiConversationRow): boolean {
  if (original.sessionId !== incoming.sessionId || original.agentId !== incoming.agentId || original.kind !== incoming.kind)
    return false
  if (original.kind === 'item' && incoming.kind === 'item') {
    return original.role === incoming.role && sameKimiConversationItemIdentity(original, incoming)
      && (original.turnContext === undefined || incoming.turnContext?.[FIELD.Kind] === original.turnContext[FIELD.Kind])
  }
  if (original.kind === 'turn' && incoming.kind === 'turn') {
    return original.turnId === incoming.turnId && original.turn[FIELD.Ordinal] === incoming.turn[FIELD.Ordinal] && original.turn[FIELD.Kind] === incoming.turn[FIELD.Kind]
  }
  if (original.kind === 'retry' && incoming.kind === 'retry') {
    return original.turnId === incoming.turnId && original.stepIndex === incoming.stepIndex && original.retry[FIELD.NextAttempt] === incoming.retry[FIELD.NextAttempt]
  }
  return false
}

type KimiConversationItemRow = Extract<KimiConversationRow, { kind: 'item' }>

function sameKimiConversationItemIdentity(left: KimiConversationItemRow, right: KimiConversationItemRow): boolean {
  return left.sessionId === right.sessionId && left.agentId === right.agentId && left.turnId === right.turnId
    && left.item[FIELD.ItemID] === right.item[FIELD.ItemID] && left.item[FIELD.Type] === right.item[FIELD.Type]
    && (left.item[FIELD.StepIndex] === undefined || right.item[FIELD.StepIndex] === undefined || left.item[FIELD.StepIndex] === right.item[FIELD.StepIndex])
    && (left.turnContext === undefined || right.turnContext === undefined || left.turnContext[FIELD.Kind] === right.turnContext[FIELD.Kind])
}

/** Pair only an earlier tool declaration with the same native tool item. */
export function kimiConversationToolsMatch(left: KimiConversationRow, right: KimiConversationRow | null): boolean {
  return left.kind === 'item' && right?.kind === 'item' && right.role === ROLE.Request
    && left.item[FIELD.Type] === ITEM.ToolCall && sameKimiConversationItemIdentity(left, right)
}
