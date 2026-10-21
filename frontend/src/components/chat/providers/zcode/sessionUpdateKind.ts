import { ZCODE_MICROCOMPACT_STRATEGY, ZCODE_NETWORK_STATUS_PREFIX } from '~/generated/contracts/zcode-protocol'
import { pickString } from '~/lib/jsonPick'

/**
 * The one shape a `session.updated` payload carries.
 *
 * `session.updated` is the app-server's catch-all: every internal event with no
 * explicit wire mapping becomes one, with a free-form payload. The shapes are told
 * apart by which fields they carry. This list and its order MIRROR the worker's
 * `zcodeSessionUpdatedPayload.kind`; a divergence classifies a row differently on
 * the two sides of the boundary.
 */
export type ZCodeSessionUpdateKind
  = | 'drop'
    | 'networkStatus'
    | 'backgroundTask'
    | 'subagent'
    | 'modelResponse'
    | 'goalVerification'
    | 'goalChange'
    | 'modeChange'
    | 'compaction'
    | 'microcompact'
    | 'modelSelected'
    | 'hookRun'
    | 'hookAdmission'
    | 'steerDiscarded'

/**
 * The row that the worker writes for a `session.updated` shape:
 *
 *   - `notification`: a notification row, which the notification reader words
 *     and which joins a notification thread.
 *   - `reply`: an assistant row through PersistMessage. Only the main reply of a
 *     model response has text for the transcript.
 *   - `unknown`: the current worker stores no row for this shape.
 */
export type ZCodeSessionUpdateRow = 'notification' | 'reply' | 'unknown'

/**
 * The row of each `session.updated` shape. The per-row classification and the
 * thread check both read this one table, so the two cannot disagree about a kind.
 */
export const ZCODE_SESSION_UPDATE_ROWS: Readonly<Record<ZCodeSessionUpdateKind, ZCodeSessionUpdateRow>> = {
  // `drop` carries telemetry. A background task belongs to the registry, and a
  // subagent update to its own transcript. The goal change drives the goal card,
  // and the worker writes the neutral `goal_updated` row for its transition. The
  // mode change drives the settings.
  drop: 'unknown',
  networkStatus: 'notification',
  backgroundTask: 'unknown',
  subagent: 'unknown',
  modelResponse: 'reply',
  goalVerification: 'notification',
  goalChange: 'unknown',
  modeChange: 'unknown',
  compaction: 'notification',
  microcompact: 'notification',
  modelSelected: 'notification',
  hookRun: 'notification',
  hookAdmission: 'notification',
  steerDiscarded: 'notification',
}

/** Whether a key held a value other than JSON null. */
function present(value: unknown): boolean {
  return value !== undefined && value !== null
}

/**
 * Tell the `session.updated` shapes apart by which fields they carry, in order of
 * specificity -- never on `status`, `toolCallId`, `requestId`, `source`, `mode` or
 * `turnId` alone. The worker's `kind()` applies the same order, and the two read
 * the network status prefix and the microcompact strategy from one contract.
 */
export function zcodeSessionUpdateKind(payload: Record<string, unknown>): ZCodeSessionUpdateKind {
  if (typeof payload.type === 'string' && payload.type.startsWith(ZCODE_NETWORK_STATUS_PREFIX.Model) && present(payload.requestId))
    return 'networkStatus'
  if (pickString(payload, 'taskId') !== '')
    return 'backgroundTask'
  if (pickString(payload, 'agentId') !== '' && pickString(payload, 'parentToolCallId') !== '')
    return 'subagent'
  if (typeof payload.content === 'string' && pickString(payload, 'stopReason') !== '')
    return 'modelResponse'
  if (present(payload.verificationId) && present(payload.targetId))
    return 'goalVerification'
  // The `target` KEY marks a goal change, and it is present as an object or as
  // null: `cleared` states its removal through the null.
  if (present(payload.action) && present(payload.source) && payload.target !== undefined)
    return 'goalChange'
  if (typeof payload.mode === 'string' && payload.mode !== '' && typeof payload.planEnabled === 'boolean')
    return 'modeChange'
  if (present(payload.operationId) && present(payload.trigger) && present(payload.status))
    return 'compaction'
  if (payload.strategy === ZCODE_MICROCOMPACT_STRATEGY.LocalToolResultClear)
    return 'microcompact'
  if (present(payload.modelSelection))
    return 'modelSelected'
  if (present(payload.hookRunId) && present(payload.hookInvocationId))
    return 'hookRun'
  if (present(payload.pendingCount) && present(payload.bundleDigest))
    return 'hookAdmission'
  if (present(payload.pendingInputIds) && present(payload.reason) && present(payload.targetTurnId))
    return 'steerDiscarded'
  return 'drop'
}
