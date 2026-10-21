import { describe, expect, it } from 'vitest'
import {
  ZCODE_COMPACTION_STATUS,
  ZCODE_EVENT,
  ZCODE_GOAL_VERIFICATION_STATUS,
  ZCODE_HOOK_RUN_OUTCOME,
  ZCODE_MICROCOMPACT_STRATEGY,
  ZCODE_NETWORK_STATUS,
} from '~/generated/contracts/zcode-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { input } from '../../testUtils'
import { zcodeCompactionBoundary, zcodeNotificationEntry } from './notification'

/** A persisted `session.updated` row. */
function sessionUpdated(payload: Record<string, unknown>): Record<string, unknown> {
  return { type: ZCODE_EVENT.SessionUpdated, payload, sessionId: 's-1', seq: 1 }
}

describe('zcodeNotificationEntry', () => {
  it('reads stored hooks without the obsolete visibility filter', () => {
    expect(zcodeNotificationEntry(sessionUpdated({
      hookRunId: 'h1',
      hookInvocationId: 'i1',
      hookEventName: 'Stop',
      outcome: 'blocked',
      blockReason: 'Continue the work',
    }))).toEqual([{ kind: 'hook', outcome: 'blocked', event: 'Stop', detail: 'Continue the work' }])
  })

  it('reads compaction outcomes and ignores stored starts', () => {
    for (const status of ['started', 'retrying']) {
      expect(zcodeNotificationEntry(sessionUpdated({ operationId: 'c1', trigger: 'auto', status }))).toEqual([])
    }
    expect(zcodeNotificationEntry(sessionUpdated({ operationId: 'c1', trigger: 'auto', status: 'interrupted' })))
      .toEqual([{ kind: 'compaction', phase: 'end', outcome: 'aborted' }])
    expect(zcodeNotificationEntry(sessionUpdated({ operationId: 'c1', trigger: 'auto', status: 'skipped' })))
      .toEqual([{ kind: 'compaction', phase: 'end', outcome: 'skipped' }])
  })
})

describe('zcodeNotificationEntry on a session.updated row', () => {
  const verification = (status: string, extra: Record<string, unknown> = {}) => sessionUpdated({
    verificationId: 'v1',
    targetId: 'g1',
    goalIteration: 2,
    status,
    ...extra,
  })

  it('words the goal check verdicts', () => {
    expect(zcodeNotificationEntry(verification(ZCODE_GOAL_VERIFICATION_STATUS.Completed, {
      verification: { passed: true, reason: 'The final commit has a green build.' },
    }))).toEqual([{ kind: 'text', text: 'Iteration 2 · Goal complete, ending task — The final commit has a green build.' }])
    expect(zcodeNotificationEntry(verification(ZCODE_GOAL_VERIFICATION_STATUS.Completed, {
      verification: { passed: false, reason: 'The tests fail.', nextAction: 'Run the tests' },
    }))).toEqual([{ kind: 'text', text: 'Iteration 2 · Goal incomplete, continuing — The tests fail. Next: Run the tests' }])
    expect(zcodeNotificationEntry(verification(ZCODE_GOAL_VERIFICATION_STATUS.FailedClosed, {
      verification: { passed: false, reason: 'The verifier timed out.' },
    }))).toEqual([{ kind: 'text', text: 'Iteration 2 · The goal check could not run, so the goal ends — The verifier timed out.' }])
    expect(zcodeNotificationEntry(verification(ZCODE_GOAL_VERIFICATION_STATUS.Cancelled)))
      .toEqual([{ kind: 'text', text: 'Iteration 2 · Goal check cancelled; the goal is paused' }])
  })

  // ZCode continues its goal loop only with a next action. A failed check with no
  // next action ends the loop ("target.continuation.skipped_no_next_action") and
  // leaves the goal active, so "continuing" would state a turn that never comes.
  it('does not claim that the goal continues when the failed check states no next action', () => {
    for (const nextAction of [undefined, '', '   ', null]) {
      expect(zcodeNotificationEntry(verification(ZCODE_GOAL_VERIFICATION_STATUS.Completed, {
        verification: { passed: false, reason: 'The tests fail.', ...(nextAction === undefined ? {} : { nextAction }) },
      })), String(nextAction)).toEqual([{ kind: 'text', text: 'Iteration 2 · Goal incomplete; the goal stays active — The tests fail.' }])
    }
  })

  // An empty reason states nothing, so the sentence ends without a dangling dash.
  it('omits the reason clause when the check states no reason', () => {
    expect(zcodeNotificationEntry(verification(ZCODE_GOAL_VERIFICATION_STATUS.Completed, {
      verification: { passed: true, reason: '' },
    }))).toEqual([{ kind: 'text', text: 'Iteration 2 · Goal complete, ending task' }])
    expect(zcodeNotificationEntry(verification(ZCODE_GOAL_VERIFICATION_STATUS.Completed, {
      verification: { passed: false, reason: '  ', nextAction: 'Run the tests' },
    }))).toEqual([{ kind: 'text', text: 'Iteration 2 · Goal incomplete, continuing. Next: Run the tests' }])
    expect(zcodeNotificationEntry(verification(ZCODE_GOAL_VERIFICATION_STATUS.Completed, {
      verification: { passed: false },
    }))).toEqual([{ kind: 'text', text: 'Iteration 2 · Goal incomplete; the goal stays active' }])
    expect(zcodeNotificationEntry(verification(ZCODE_GOAL_VERIFICATION_STATUS.FailedClosed)))
      .toEqual([{ kind: 'text', text: 'Iteration 2 · The goal check could not run, so the goal ends' }])
  })

  // ZCode's own client reads an absent iteration as the first one.
  it('counts an absent iteration as the first one', () => {
    expect(zcodeNotificationEntry(sessionUpdated({
      verificationId: 'v1',
      targetId: 'g1',
      status: ZCODE_GOAL_VERIFICATION_STATUS.Cancelled,
    }))).toEqual([{ kind: 'text', text: 'Iteration 1 · Goal check cancelled; the goal is paused' }])
    expect(zcodeNotificationEntry(verification(ZCODE_GOAL_VERIFICATION_STATUS.Cancelled, { goalIteration: -1 })))
      .toEqual([{ kind: 'text', text: 'Iteration 1 · Goal check cancelled; the goal is paused' }])
  })

  it('hides the start of a goal check', () => {
    expect(zcodeNotificationEntry(verification(ZCODE_GOAL_VERIFICATION_STATUS.Started))).toEqual([])
  })

  it('words every state of the compaction lifecycle', () => {
    const lifecycle = (status: string, extra: Record<string, unknown> = {}) => sessionUpdated({
      operationId: 'c1',
      status,
      trigger: 'auto',
      phase: 'standalone_turn',
      ...extra,
    })
    expect(zcodeNotificationEntry(lifecycle(ZCODE_COMPACTION_STATUS.Started)))
      .toEqual([])
    expect(zcodeNotificationEntry(lifecycle(ZCODE_COMPACTION_STATUS.Retrying, { attempt: 2, maxAttempts: 5 })))
      .toEqual([])
    expect(zcodeNotificationEntry(lifecycle(ZCODE_COMPACTION_STATUS.Skipped)))
      .toEqual([{ kind: 'compaction', phase: 'end', outcome: 'skipped' }])
    expect(zcodeNotificationEntry(lifecycle(ZCODE_COMPACTION_STATUS.Completed, {
      preCompactTokenCount: 180000,
      postCompactTokenCount: 12000,
    }))).toEqual([{ kind: 'compaction', phase: 'end', detail: { trigger: 'auto', pre: 180000, post: 12000 } }])
    expect(zcodeNotificationEntry(lifecycle(ZCODE_COMPACTION_STATUS.Failed)))
      .toEqual([{ kind: 'compaction', phase: 'end', outcome: 'failed', error: 'the provider reported a failure' }])
    expect(zcodeNotificationEntry(lifecycle(ZCODE_COMPACTION_STATUS.Interrupted)))
      .toEqual([{ kind: 'compaction', phase: 'end', outcome: 'aborted' }])
  })

  // A failed compaction states why it failed. The reason is the error that the
  // reader must see; the generic phrase covers only a failure with no reason.
  it('states the reason of a failed compaction', () => {
    const failed = (extra: Record<string, unknown>) => sessionUpdated({
      operationId: 'c1',
      status: ZCODE_COMPACTION_STATUS.Failed,
      trigger: 'auto',
      ...extra,
    })
    expect(zcodeNotificationEntry(failed({ reason: 'The summary model is unavailable.' })))
      .toEqual([{ kind: 'compaction', phase: 'end', outcome: 'failed', error: 'The summary model is unavailable.' }])
    expect(zcodeNotificationEntry(failed({ reason: '  ' })))
      .toEqual([{ kind: 'compaction', phase: 'end', outcome: 'failed', error: 'the provider reported a failure' }])
  })

  // `truePostCompactTokenCount` is the count that ZCode measured after the
  // rewrite, and ZCode's own client prefers it to the estimate.
  it('prefers the measured post-compaction count to the estimate', () => {
    const completed = (extra: Record<string, unknown>) => sessionUpdated({
      operationId: 'c1',
      status: ZCODE_COMPACTION_STATUS.Completed,
      trigger: 'auto',
      preCompactTokenCount: 180000,
      ...extra,
    })
    expect(zcodeNotificationEntry(completed({ postCompactTokenCount: 12000, truePostCompactTokenCount: 15000 })))
      .toEqual([{ kind: 'compaction', phase: 'end', detail: { trigger: 'auto', pre: 180000, post: 15000 } }])
    expect(zcodeNotificationEntry(completed({ postCompactTokenCount: 12000, truePostCompactTokenCount: -1 })))
      .toEqual([{ kind: 'compaction', phase: 'end', detail: { trigger: 'auto', pre: 180000, post: 12000 } }])
    expect(zcodeNotificationEntry(completed({ truePostCompactTokenCount: 0 })))
      .toEqual([{ kind: 'compaction', phase: 'end', detail: { trigger: 'auto', pre: 180000, post: 0 } }])
  })

  it('words a microcompact as its own boundary', () => {
    expect(zcodeNotificationEntry(sessionUpdated({ strategy: ZCODE_MICROCOMPACT_STRATEGY.LocalToolResultClear, tokensSaved: 4000 })))
      .toEqual([{ kind: 'compaction', phase: 'end', micro: true }])
  })

  it('words the scheduled retry and drops the other network statuses', () => {
    expect(zcodeNotificationEntry(sessionUpdated({
      type: ZCODE_NETWORK_STATUS.RetryScheduled,
      requestId: 'q1',
      _meta: { zcode: { apiRetry: { kind: 'api_retry', attempt: 2, maxRetries: 5, retryDelayMs: 4000, errorStatus: 529, error: 'overloaded' } } },
    }))).toEqual([{ kind: 'retry', scope: 'api', attempt: 2, maxAttempts: 5, delayMs: 4000, error: 'overloaded' }])
    expect(zcodeNotificationEntry(sessionUpdated({
      type: 'model_request_started',
      requestId: 'q1',
      baseURL: 'https://api.example',
    }))).toEqual([])
  })

  // A side query is a model call that the app-server makes for itself. Its retry
  // follows the rule of its reply: only the main turn is the user's.
  it('reads a stored retry without a query-source filter', () => {
    const retry = (querySource: unknown) => sessionUpdated({
      type: ZCODE_NETWORK_STATUS.RetryScheduled,
      requestId: 'q1',
      querySource,
      _meta: { zcode: { apiRetry: { kind: 'api_retry', attempt: 2, maxRetries: 5, error: 'overloaded' } } },
    })
    expect(zcodeNotificationEntry(retry('main_turn')))
      .toEqual([{ kind: 'retry', scope: 'api', attempt: 2, maxAttempts: 5, error: 'overloaded' }])
    for (const querySource of ['target_completion_verification', 'goal_summary_title', 'compact', 5, null]) {
      expect(zcodeNotificationEntry(retry(querySource)), String(querySource))
        .toEqual([{ kind: 'retry', scope: 'api', attempt: 2, maxAttempts: 5, error: 'overloaded' }])
    }
  })

  it('words the registry fallback and hides an unnameable switch', () => {
    const fallback = (previousModelSelection: unknown) => sessionUpdated({
      modelSelection: { providerId: 'p', modelId: 'glm-5.3' },
      previousModelSelection,
      origin: 'registryFallback',
    })
    expect(zcodeNotificationEntry(fallback({ providerId: 'p', modelId: 'glm-6' })))
      .toEqual([{ kind: 'text', text: 'Switched from glm-6 to glm-5.3' }])
    expect(zcodeNotificationEntry(fallback(null))).toEqual([])
  })

  it('words a client-visible hook failure only', () => {
    const hook = (payload: Record<string, unknown>) => sessionUpdated({
      hookRunId: 'h1',
      hookInvocationId: 'i1',
      hookEventName: 'PreToolUse',
      ...payload,
    })
    expect(zcodeNotificationEntry(hook({
      outcome: ZCODE_HOOK_RUN_OUTCOME.Failed,
      descriptor: { clientVisible: true, sourceKind: 'user' },
      errorMessage: 'boom',
    }))).toEqual([{ kind: 'hook', outcome: 'failed', event: 'PreToolUse', detail: 'boom' }])
    expect(zcodeNotificationEntry(hook({
      outcome: ZCODE_HOOK_RUN_OUTCOME.Blocked,
      descriptor: { clientVisible: true, sourceKind: 'user' },
      blockReason: 'policy',
    }))).toEqual([{ kind: 'hook', outcome: 'blocked', event: 'PreToolUse', detail: 'policy' }])
    expect(zcodeNotificationEntry(hook({
      outcome: ZCODE_HOOK_RUN_OUTCOME.Failed,
      descriptor: { clientVisible: false, sourceKind: 'internal' },
      errorMessage: 'boom',
    }))).toEqual([{ kind: 'hook', outcome: 'failed', event: 'PreToolUse', detail: 'boom' }])
  })

  // A hook that ran past its time limit did not do its work. A cancelled hook
  // stopped with its turn, which is not a failure.
  it('words a hook timeout and drops a cancellation', () => {
    const hook = (outcome: string) => sessionUpdated({
      hookRunId: 'h1',
      hookInvocationId: 'i1',
      hookEventName: 'PreToolUse',
      outcome,
      descriptor: { clientVisible: true, sourceKind: 'user' },
      errorMessage: 'Hook timed out after 60000ms',
    })
    expect(zcodeNotificationEntry(hook(ZCODE_HOOK_RUN_OUTCOME.TimedOut)))
      .toEqual([{ kind: 'hook', outcome: 'timed-out', event: 'PreToolUse' }])
    expect(zcodeNotificationEntry(hook('cancelled'))).toEqual([])
  })

  // An empty message states nothing, so the sentence ends at the verb rather than
  // at a dangling colon. A run with no event name is "a hook".
  it('ends the hook sentence at the verb when the run states no message', () => {
    const hook = (payload: Record<string, unknown>) => sessionUpdated({
      hookRunId: 'h1',
      hookInvocationId: 'i1',
      descriptor: { clientVisible: true, sourceKind: 'user' },
      ...payload,
    })
    expect(zcodeNotificationEntry(hook({ hookEventName: 'Stop', outcome: ZCODE_HOOK_RUN_OUTCOME.Failed, errorMessage: '' })))
      .toEqual([{ kind: 'hook', outcome: 'failed', event: 'Stop' }])
    expect(zcodeNotificationEntry(hook({ hookEventName: 'Stop', outcome: ZCODE_HOOK_RUN_OUTCOME.Blocked })))
      .toEqual([{ kind: 'hook', outcome: 'blocked', event: 'Stop' }])
    expect(zcodeNotificationEntry(hook({ outcome: ZCODE_HOOK_RUN_OUTCOME.Failed, errorMessage: 'boom' })))
      .toEqual([{ kind: 'hook', outcome: 'failed', detail: 'boom' }])
    expect(zcodeNotificationEntry(hook({ outcome: ZCODE_HOOK_RUN_OUTCOME.TimedOut })))
      .toEqual([{ kind: 'hook', outcome: 'timed-out' }])
  })

  it('words the workspace hook admission when hooks are pending', () => {
    expect(zcodeNotificationEntry(sessionUpdated({ pendingCount: 2, bundleDigest: 'abc' })))
      .toEqual([{ kind: 'text', text: '2 workspace hooks pending review; disabled for this session' }])
    expect(zcodeNotificationEntry(sessionUpdated({ pendingCount: 1, bundleDigest: 'abc' })))
      .toEqual([{ kind: 'text', text: '1 workspace hook pending review; disabled for this session' }])
    expect(zcodeNotificationEntry(sessionUpdated({ pendingCount: 0, bundleDigest: 'abc' })))
      .toEqual([{ kind: 'text', text: '0 workspace hooks pending review; disabled for this session' }])
  })

  // The reason is a wire word. A known word reads as a phrase; an unknown word
  // stays off the row rather than showing a raw token.
  it('words a discarded queued input with a phrase for its reason', () => {
    const discarded = (reason: unknown) => sessionUpdated({ pendingInputIds: ['p1'], reason, targetTurnId: 'turn_1' })
    expect(zcodeNotificationEntry(discarded('user_removed')))
      .toEqual([{ kind: 'text', text: 'Queued input discarded (you removed it)' }])
    expect(zcodeNotificationEntry(discarded('session_resumed')))
      .toEqual([{ kind: 'text', text: 'Queued input discarded (the session resumed)' }])
    expect(zcodeNotificationEntry(discarded('turn_failed')))
      .toEqual([{ kind: 'text', text: 'Queued input discarded (the turn failed)' }])
    for (const reason of ['a_reason_from_a_later_release', 'toString', 5]) {
      expect(zcodeNotificationEntry(discarded(reason)), String(reason))
        .toEqual([{ kind: 'text', text: 'Queued input discarded' }])
    }
  })

  // The two status vocabularies are closed. A word outside one, or no word at
  // all, draws nothing rather than a sentence that misreads the row.
  const compaction = (payload: Record<string, unknown>) => sessionUpdated({
    operationId: 'c1',
    trigger: 'auto',
    phase: 'standalone_turn',
    ...payload,
  })

  it('states nothing for a goal check word outside its vocabulary', () => {
    for (const status of ['', 'failed', 'a_word_from_a_later_release']) {
      expect(zcodeNotificationEntry(verification(status)), status).toEqual([])
    }
    expect(zcodeNotificationEntry(sessionUpdated({ verificationId: 'v1', targetId: 'g1', goalIteration: 2 }))).toEqual([])
  })

  it('states nothing for a compaction word outside its vocabulary', () => {
    for (const status of ['', 'a_word_from_a_later_release']) {
      expect(zcodeNotificationEntry(compaction({ status })), status).toEqual([])
    }
    expect(zcodeNotificationEntry(compaction({}))).toEqual([])
  })

  // A counter a provider reports out of range renders as absent, never as a NaN
  // and never as a negative count.
  it('omits a retry counter that is not a finite non-negative number', () => {
    expect(zcodeNotificationEntry(compaction({ status: ZCODE_COMPACTION_STATUS.Retrying, attempt: -1, maxAttempts: Number.NaN })))
      .toEqual([])
  })

  // Shapes the entry builder suppresses: no retry record on the network status,
  // a hook run the user need not see, a model switch that the session made, and
  // a steer bookkeeping record with no reason.
  it('reads stored model selections and skips unrecognized shapes', () => {
    expect(zcodeNotificationEntry(sessionUpdated({ type: ZCODE_NETWORK_STATUS.RetryScheduled, requestId: 'q1' }))).toEqual([])
    expect(zcodeNotificationEntry(sessionUpdated({
      hookRunId: 'h1',
      hookInvocationId: 'i1',
      hookEventName: 'PreToolUse',
      outcome: 'success',
      descriptor: { clientVisible: true, sourceKind: 'user' },
    }))).toEqual([])
    expect(zcodeNotificationEntry(sessionUpdated({
      modelSelection: { providerId: 'p', modelId: 'glm-5.3' },
      previousModelSelection: { providerId: 'p', modelId: 'glm-6' },
    }))).toEqual([{ kind: 'text', text: 'Switched from glm-6 to glm-5.3' }])
    expect(zcodeNotificationEntry(sessionUpdated({ pendingInputIds: ['p1'], targetTurnId: 'turn_1' }))).toEqual([])
  })
})

describe('zcodeCompactionBoundary', () => {
  const parse = (msg: Record<string, unknown>) => input(msg, undefined, AgentProvider.ZCODE)
  const lifecycle = (status: string, extra: Record<string, unknown> = {}) => sessionUpdated({
    operationId: 'c1',
    status,
    trigger: 'auto',
    phase: 'standalone_turn',
    ...extra,
  })

  // The context meter reads the size after the rewrite from this hook, so an
  // automatic compaction moves the meter at once.
  it('reads the size of a completed compaction', () => {
    expect(zcodeCompactionBoundary(parse(lifecycle(ZCODE_COMPACTION_STATUS.Completed, {
      preCompactTokenCount: 180000,
      postCompactTokenCount: 12000,
      truePostCompactTokenCount: 15000,
    })))).toEqual({ trigger: 'auto', pre: 180000, post: 15000 })
    expect(zcodeCompactionBoundary(parse(lifecycle(ZCODE_COMPACTION_STATUS.Completed))))
      .toEqual({ trigger: 'auto' })
  })

  // Every other state has no size after the rewrite, and neither has a
  // microcompact or a stored `state.updated` row.
  it('answers null for a frame that is not a completed compaction', () => {
    for (const status of [
      ZCODE_COMPACTION_STATUS.Started,
      ZCODE_COMPACTION_STATUS.Retrying,
      ZCODE_COMPACTION_STATUS.Skipped,
      ZCODE_COMPACTION_STATUS.Failed,
      ZCODE_COMPACTION_STATUS.Interrupted,
    ]) {
      expect(zcodeCompactionBoundary(parse(lifecycle(status, { postCompactTokenCount: 12000 }))), status).toBeNull()
    }
    expect(zcodeCompactionBoundary(parse(sessionUpdated({ strategy: ZCODE_MICROCOMPACT_STRATEGY.LocalToolResultClear, tokensSaved: 4000 }))))
      .toBeNull()
    expect(zcodeCompactionBoundary(parse({ type: ZCODE_EVENT.TurnCompleted, payload: {} }))).toBeNull()
  })
})
