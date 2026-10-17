import type { ZCodeSessionUpdateRow } from './sessionUpdateKind'
import { describe, expect, it } from 'vitest'
import { ZCODE_MICROCOMPACT_STRATEGY, ZCODE_NETWORK_STATUS } from '~/generated/contracts/zcode-protocol'
import { ZCODE_SESSION_UPDATE_ROWS, zcodeSessionUpdateKind } from './sessionUpdateKind'

/**
 * One wire payload for each `session.updated` shape, and the row that the worker
 * writes for that shape.
 *
 * `handleZCodeSessionUpdated` in the worker persists the notification shapes as
 * notification rows. A model response becomes an assistant row through
 * PersistMessage, and the remaining shapes drive worker state alone. The table
 * decides both the per-row classification and whether a stored row joins a
 * notification thread. A wrong answer therefore hides a row that the worker kept,
 * or threads a row that the worker never wrote. The kind dispatch itself is in
 * the shared corpus; this pins the row decision that the corpus does not carry.
 */
const ROWS: { name: string, payload: Record<string, unknown>, row: ZCodeSessionUpdateRow }[] = [
  { name: 'a network status', payload: { type: ZCODE_NETWORK_STATUS.RetryScheduled, requestId: 'q1' }, row: 'notification' },
  { name: 'a background task', payload: { taskId: 'task-1' }, row: 'none' },
  { name: 'a subagent update', payload: { agentId: 'child-1', parentToolCallId: 'spawn-1' }, row: 'none' },
  { name: 'a model response', payload: { content: 'Hello', stopReason: 'stop' }, row: 'reply' },
  { name: 'a goal check', payload: { verificationId: 'v1', targetId: 'g1' }, row: 'notification' },
  { name: 'a goal change', payload: { action: 'cleared', source: 'command', target: null }, row: 'none' },
  { name: 'a mode change', payload: { mode: 'plan', planEnabled: true }, row: 'none' },
  { name: 'a compaction lifecycle', payload: { operationId: 'c1', trigger: 'auto', status: 'completed' }, row: 'notification' },
  { name: 'a microcompact boundary', payload: { strategy: ZCODE_MICROCOMPACT_STRATEGY.LocalToolResultClear }, row: 'notification' },
  { name: 'a model switch', payload: { modelSelection: { modelId: 'm1' } }, row: 'notification' },
  { name: 'a hook run', payload: { hookRunId: 'h1', hookInvocationId: 'i1' }, row: 'notification' },
  { name: 'a workspace hook admission', payload: { pendingCount: 2, bundleDigest: 'abc' }, row: 'notification' },
  { name: 'a discarded queued input', payload: { pendingInputIds: ['p1'], reason: 'user_removed', targetTurnId: 'turn_1' }, row: 'notification' },
  { name: 'a telemetry shape', payload: { messageCount: 3 }, row: 'none' },
]

describe('ZCODE_SESSION_UPDATE_ROWS', () => {
  it.each(ROWS)('$name is read as the $row row', ({ payload, row }) => {
    expect(ZCODE_SESSION_UPDATE_ROWS[zcodeSessionUpdateKind(payload)]).toBe(row)
  })
})

describe('zcodeSessionUpdateKind on wire values outside the vocabularies', () => {
  // The network status vocabulary is the `model_` type word beside a request id.
  // A word outside it and a non-string word are not network statuses, whatever
  // else the payload carries.
  it('drops a type word outside the model_ prefix even beside a request id', () => {
    expect(zcodeSessionUpdateKind({ type: 'session_anomaly_warning', requestId: 'q1' })).toBe('drop')
    expect(zcodeSessionUpdateKind({ type: 5, requestId: 'q1' })).toBe('drop')
  })

  // The microcompact vocabulary is the one strategy word. A payload that states
  // no word at all takes the same answer as one that states a wrong word.
  it('drops a microcompact boundary that states no strategy', () => {
    expect(zcodeSessionUpdateKind({ tokensSaved: 4000 })).toBe('drop')
  })

  // The two presence rules differ on purpose. `taskId` and its kin read as
  // strings, so an empty or non-string value counts as absent. `verificationId`
  // and its kin read as raw keys, so any value other than JSON null is present.
  it('reads an empty task id as absent and an empty verification id as present', () => {
    expect(zcodeSessionUpdateKind({ taskId: '' })).toBe('drop')
    expect(zcodeSessionUpdateKind({ taskId: 5 })).toBe('drop')
    expect(zcodeSessionUpdateKind({ verificationId: '', targetId: '' })).toBe('goalVerification')
  })

  // The `target` KEY marks a goal change, present as an object or as JSON null.
  // A payload that omits the key states no goal change at all.
  it('drops a goal change with no target key', () => {
    expect(zcodeSessionUpdateKind({ action: 'cleared', source: 'command' })).toBe('drop')
    expect(zcodeSessionUpdateKind({ action: 'cleared', source: 'command', target: null })).toBe('goalChange')
  })
})
