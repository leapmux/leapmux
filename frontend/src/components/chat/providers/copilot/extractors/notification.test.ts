import { describe, expect, it } from 'vitest'
import { COPILOT_EVENT, COPILOT_METHOD, COPILOT_PERMISSION_DECISION_SOURCE, COPILOT_PERMISSION_OUTCOME } from '~/generated/contracts/copilot-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { COMPACTING_LABEL, compactionContextTokens, flattenNotificationEntries } from '../../../notificationEntries'
import { input } from '../../testUtils'
import { classifyCopilotMessage } from '../classification'
import { copilotCompactionBoundary, copilotNotificationEntry, describeCopilotNotification } from './notification'
// Side-effect import: `compactionContextTokens` dispatches through the registry.
import '~/components/chat/providers'

function row(type: string, data: Record<string, unknown> = {}): Record<string, unknown> {
  return { method: COPILOT_METHOD.SessionEvent, params: { sessionId: 's', event: { id: 'e', type, agentId: '', data } } }
}

/**
 * One `permission.completed`, in the runtime's own shape.
 *
 * `result` is an OBJECT whose `kind` states the outcome and whose remaining fields
 * differ per variant (`rules` on a rule refusal, `feedback` on an interactive one).
 * A fixture that made `result` a plain word would agree with a reader that made the
 * same mistake, and neither would match the wire.
 */
function completed(result: Record<string, unknown>, decisionSource?: string): Record<string, unknown> {
  return row(COPILOT_EVENT.PermissionCompleted, { requestId: 'r', result, ...(decisionSource ? { decisionSource } : {}) })
}

/**
 * A permission the runtime refused ON ITS OWN reaches no control surface: there is no
 * banner, no answer and no saved response row. `permission.completed` is the only place
 * the refusal is ever stated, so hiding the whole event left the reader with a tool call
 * that failed and nothing that said why.
 */
describe('a permission the reader never saw', () => {
  it.each([
    [COPILOT_PERMISSION_OUTCOME.DeniedByRules, 'Denied by the permission rules'],
    [COPILOT_PERMISSION_OUTCOME.DeniedNoApprovalRuleAndCouldNotRequestFromUser, 'Denied: no approval rule matched, and the runtime could not ask'],
    [COPILOT_PERMISSION_OUTCOME.DeniedByContentExclusionPolicy, 'Denied by the content exclusion policy'],
    [COPILOT_PERMISSION_OUTCOME.DeniedByPermissionRequestHook, 'Denied by a permission hook'],
  ])('states the %s refusal', (kind, sentence) => {
    expect(describeCopilotNotification(completed({ kind }))).toBe(sentence)
    expect(classifyCopilotMessage({ parentObject: completed({ kind }) } as never).kind).toBe('notification')
  })

  // The rules are the useful half of a rule refusal: the kind and the argument
  // together say WHICH rule to change.
  it('names the rules that refused', () => {
    const result = {
      kind: COPILOT_PERMISSION_OUTCOME.DeniedByRules,
      rules: [{ kind: 'Shell', argument: 'rm' }, { kind: 'read', argument: null }],
    }
    expect(describeCopilotNotification(completed(result))).toBe('Denied by the permission rules: Shell: rm, read')
  })

  // A rule list the runtime omitted, or one holding nothing readable, still leaves a
  // refusal worth stating -- so the sentence stands on its own.
  it.each([
    ['omits the rules', {}],
    ['states an empty list', { rules: [] }],
    ['states rules with no kind', { rules: [{ argument: 'rm' }, 'not an object'] }],
  ])('states the bare refusal when the runtime %s', (_name, extra) => {
    expect(describeCopilotNotification(completed({ kind: COPILOT_PERMISSION_OUTCOME.DeniedByRules, ...extra })))
      .toBe('Denied by the permission rules')
  })
})

/**
 * The five outcomes below record an answer the READER gave, and the saved
 * control-response row already states each one. A second row beside it would say the
 * same thing twice.
 */
describe('a permission the reader answered', () => {
  it.each([
    COPILOT_PERMISSION_OUTCOME.Approved,
    COPILOT_PERMISSION_OUTCOME.ApprovedForSession,
    COPILOT_PERMISSION_OUTCOME.ApprovedForLocation,
    COPILOT_PERMISSION_OUTCOME.Cancelled,
    COPILOT_PERMISSION_OUTCOME.DeniedInteractivelyByUser,
  ])('hides the %s outcome', (kind) => {
    expect(describeCopilotNotification(completed({ kind }))).toBeNull()
    expect(classifyCopilotMessage({ parentObject: completed({ kind }) } as never).kind).toBe('hidden')
  })

  // An outcome this build cannot read states nothing a reader can act on, so it hides
  // rather than drawing a raw-JSON bubble. The middle case is the one that used to
  // pass for a whole outcome vocabulary: `result` as a bare word matches nothing.
  it.each([
    ['an outcome word it does not know', completed({ kind: 'something-newer' })],
    ['a result that is a word rather than an object', row(COPILOT_EVENT.PermissionCompleted, { result: 'denied-by-rules' })],
    ['a completion with no result at all', row(COPILOT_EVENT.PermissionCompleted)],
  ])('hides %s', (_name, payload) => {
    expect(describeCopilotNotification(payload)).toBeNull()
  })
})

/**
 * An approval the READER never gave.
 *
 * An assisted-approval judge, a host policy, an unattended fallback and a replayed
 * authorization record all return the SAME `result` a person does, with no control
 * surface and no saved response row behind them. `decisionSource` is the only field
 * that separates them, so without it a tool ran with elevated permission and nothing
 * on screen said who allowed it.
 */
describe('a permission nobody was asked about', () => {
  it.each([
    [COPILOT_PERMISSION_DECISION_SOURCE.AssistedApproval, 'Approved by the assisted-approval judge'],
    [COPILOT_PERMISSION_DECISION_SOURCE.HostPolicy, 'Approved by a standing host policy'],
    [COPILOT_PERMISSION_DECISION_SOURCE.UnattendedFallback, 'Approved by the runtime, with nobody available to ask'],
    [COPILOT_PERMISSION_DECISION_SOURCE.AuthorizationCarryForward, 'Approved by an earlier approval of yours in this session'],
  ])('states an approval %s decided', (source, sentence) => {
    const payload = completed({ kind: COPILOT_PERMISSION_OUTCOME.Approved }, source)
    expect(describeCopilotNotification(payload)).toBe(sentence)
    expect(classifyCopilotMessage({ parentObject: payload } as never).kind).toBe('notification')
  })

  // The reader DID decide this one, so the saved control-response row states it and a
  // second row would say the same thing twice.
  it('still hides an approval the reader gave', () => {
    const payload = completed({ kind: COPILOT_PERMISSION_OUTCOME.Approved }, COPILOT_PERMISSION_DECISION_SOURCE.HumanResponse)
    expect(describeCopilotNotification(payload)).toBeNull()
    expect(classifyCopilotMessage({ parentObject: payload } as never).kind).toBe('hidden')
  })

  /**
   * Copilot states the field from 1.0.84-5, so every completion an earlier build wrote
   * omits it. Silence asserts nothing -- which is what the SDK asks for, since a
   * consumer must not read an absent field as a human decision -- and it keeps the
   * transcript of a 1.0.83 session exactly as it was.
   */
  it.each([
    COPILOT_PERMISSION_OUTCOME.Approved,
    COPILOT_PERMISSION_OUTCOME.ApprovedForSession,
    COPILOT_PERMISSION_OUTCOME.Cancelled,
  ])('says nothing about %s when the runtime states no source', (kind) => {
    expect(describeCopilotNotification(completed({ kind }))).toBeNull()
  })

  it('says nothing for a source word this build does not know', () => {
    expect(describeCopilotNotification(completed({ kind: COPILOT_PERMISSION_OUTCOME.Approved }, 'newer_source'))).toBeNull()
  })

  it('words each approve outcome with its own scope', () => {
    const byPolicy = (kind: string) => describeCopilotNotification(completed({ kind }, COPILOT_PERMISSION_DECISION_SOURCE.HostPolicy))
    expect(byPolicy(COPILOT_PERMISSION_OUTCOME.ApprovedForSession)).toBe('Approved for the session by a standing host policy')
    expect(byPolicy(COPILOT_PERMISSION_OUTCOME.ApprovedForLocation)).toBe('Approved for this location by a standing host policy')
    expect(byPolicy(COPILOT_PERMISSION_OUTCOME.DeniedInteractivelyByUser)).toBe('Denied by a standing host policy')
  })

  // A refusal the runtime made on its own already states WHY, which says more than the
  // source does. The source must not replace that sentence.
  it('keeps the rule refusal sentence whatever the source says', () => {
    const payload = completed(
      { kind: COPILOT_PERMISSION_OUTCOME.DeniedByRules, rules: [{ kind: 'Shell', argument: 'rm' }] },
      COPILOT_PERMISSION_DECISION_SOURCE.HostPolicy,
    )
    expect(describeCopilotNotification(payload)).toBe('Denied by the permission rules: Shell: rm')
  })
})

/**
 * `permission.carriedForward` records that an earlier human approval already covered
 * this proposal, so the runtime ran the tool without asking again. Nobody is asked, so
 * no control surface and no response row records it, and the transcript is the only
 * place a reader can learn that one approval admitted a second call.
 */
describe('a tool an earlier approval admitted', () => {
  const carried = row(COPILOT_EVENT.PermissionCarriedForward, {
    decisionSource: COPILOT_PERMISSION_DECISION_SOURCE.AuthorizationCarryForward,
    recordId: 'rec-1',
    requestId: 'req-1',
    toolCallId: 'call-1',
  })

  it('states the admission rather than drawing raw JSON', () => {
    expect(describeCopilotNotification(carried)).toBe('Ran under an earlier approval of yours in this session')
    expect(classifyCopilotMessage({ parentObject: carried } as never).kind).toBe('notification')
  })
})

/*
 * The worker threads consecutive notifications into ONE row, and this event reaches
 * that path only now: before the per-outcome hiding it was hidden by type and never
 * notified at all. The thread must hide when every completion in it is one the reader
 * answered, and it must keep the refusals when it holds both.
 */
describe('a thread of permission completions', () => {
  const thread = (...messages: Record<string, unknown>[]) =>
    classifyCopilotMessage({ wrapper: { old_seqs: [], messages } } as never)

  it('hides a thread whose completions the reader all answered', () => {
    expect(thread(
      completed({ kind: COPILOT_PERMISSION_OUTCOME.Approved }),
      completed({ kind: COPILOT_PERMISSION_OUTCOME.DeniedInteractivelyByUser, feedback: 'not that one' }),
    ).kind).toBe('hidden')
  })

  it('keeps the refusals and drops the answers from a mixed thread', () => {
    const answered = completed({ kind: COPILOT_PERMISSION_OUTCOME.Approved })
    const refused = completed({ kind: COPILOT_PERMISSION_OUTCOME.DeniedByRules })
    const category = thread(answered, refused)
    if (category.kind !== 'notification')
      throw new Error('a refusal the reader never saw must reach a row')
    expect(category.messages).toEqual([refused])
  })
})

/**
 * The compaction boundary, from a `session.compaction_complete` frame recorded by the
 * runtime itself.
 *
 * Every number below is the shape a real session wrote to
 * `~/.copilot/session-state/<id>/events.jsonl`, and the field names come from the
 * runtime's own `CompactionCompleteData` typing. The runtime persists the whole frame,
 * so these counts already reach the browser; the extractor read only the `phase` out of
 * it, and the context-usage grid therefore kept its pre-compaction reading until the
 * next message that carried usage.
 */
describe('copilotCompactionBoundary', () => {
  const completeData = {
    success: true,
    preCompactionTokens: 1035024,
    postCompactionTokens: 34193,
    preCompactionMessagesLength: 16,
    messagesRemoved: 14,
    tokensRemoved: 1000834,
    tokenLimit: 200000,
    trigger: 'threshold',
  }
  const parse = (msg: Record<string, unknown>) => input(msg, undefined, AgentProvider.GITHUB_COPILOT)

  it('reads the trigger and the token transition of a completed pass', () => {
    expect(copilotCompactionBoundary(parse(row(COPILOT_EVENT.SessionCompactionComplete, completeData))))
      .toStrictEqual({ trigger: 'threshold', pre: 1035024, post: 34193 })
  })

  // The precedence Copilot's own interface uses: the three parts add up to the whole
  // context window, where `postCompactionTokens` counts the conversation alone.
  it('prefers the three-part breakdown to the conversation total', () => {
    const boundary = copilotCompactionBoundary(parse(row(COPILOT_EVENT.SessionCompactionComplete, {
      ...completeData,
      systemTokens: 23927,
      conversationTokens: 8000,
      toolDefinitionsTokens: 9690,
    })))
    expect(boundary).toStrictEqual({ trigger: 'threshold', pre: 1035024, post: 41617 })
  })

  // One part is enough: the runtime sends a part only for the pass that computed it, so
  // the sum stands as soon as any of the three arrives.
  it('sums the breakdown when the runtime states one part of it', () => {
    expect(copilotCompactionBoundary(parse(row(COPILOT_EVENT.SessionCompactionComplete, { ...completeData, conversationTokens: 8000 })))?.post).toBe(8000)
  })

  // A pass that did not succeed rewrote nothing, so its counts describe a context that
  // still holds what it held before and the grid must not refresh from them.
  it('answers null for a pass that did not succeed', () => {
    expect(copilotCompactionBoundary(parse(row(COPILOT_EVENT.SessionCompactionComplete, {
      success: false,
      error: 'the model returned an empty response',
      statusCode: 500,
      tokenLimit: 200000,
      trigger: 'manual',
    })))).toBeNull()
  })

  it('answers null for the frame that opens a compaction and for every other event', () => {
    expect(copilotCompactionBoundary(parse(row(COPILOT_EVENT.SessionCompactionStart, {
      systemTokens: 23927,
      conversationTokens: 1001407,
      toolDefinitionsTokens: 9690,
      currentTokens: 1035024,
      tokenLimit: 200000,
      trigger: 'threshold',
    })))).toBeNull()
    expect(copilotCompactionBoundary(parse(row(COPILOT_EVENT.SessionContextCleared)))).toBeNull()
    expect(copilotCompactionBoundary(parse({ type: 'assistant' }))).toBeNull()
  })

  /*
   * The reader the hook exists for. `compactionContextTokens` dispatches through the
   * registry, so this states that the plugin registers the hook as well as that the
   * hook reads the frame -- an extractor nobody wires up refreshes nothing.
   */
  it('refreshes the context-usage grid through the provider registry', () => {
    expect(compactionContextTokens(parse(row(COPILOT_EVENT.SessionCompactionComplete, completeData)), AgentProvider.GITHUB_COPILOT)).toBe(34193)
    expect(compactionContextTokens(parse(row(COPILOT_EVENT.SessionCompactionStart, {})), AgentProvider.GITHUB_COPILOT)).toBeUndefined()
  })
})

/** The two rows one compaction draws, and the sentence each of them states. */
describe('copilotNotificationEntry', () => {
  const blocks = (msg: Record<string, unknown>) => flattenNotificationEntries(copilotNotificationEntry(msg))

  it('states the transition on the row that closes a compaction', () => {
    expect(blocks(row(COPILOT_EVENT.SessionCompactionComplete, {
      success: true,
      preCompactionTokens: 1035024,
      postCompactionTokens: 34193,
      trigger: 'threshold',
    }))).toStrictEqual([{ kind: 'divider', text: 'Context compacted (threshold, 1.0M → 34.2k)' }])
  })

  it('draws the spinner while a compaction runs', () => {
    expect(blocks(row(COPILOT_EVENT.SessionCompactionStart, { trigger: 'manual' })))
      .toStrictEqual([{ kind: 'divider', text: COMPACTING_LABEL, loading: true }])
  })

  // The boundary rule claims a rewrite. A pass that failed made none, so the row states
  // the reason the runtime gave instead.
  it('states the reason a pass that did not succeed gave', () => {
    expect(blocks(row(COPILOT_EVENT.SessionCompactionComplete, { success: false, error: 'the model returned an empty response' })))
      .toStrictEqual([{ kind: 'text', text: 'Compaction failed (the model returned an empty response)' }])
  })

  // The runtime states `statusCode` for a failure that carried an HTTP status and no
  // message. An empty reason would fall through to the boundary rule.
  it('falls back to the status a failure carried when it stated no message', () => {
    expect(blocks(row(COPILOT_EVENT.SessionCompactionComplete, { success: false, statusCode: 413 })))
      .toStrictEqual([{ kind: 'text', text: 'Compaction failed (HTTP 413)' }])
    expect(blocks(row(COPILOT_EVENT.SessionCompactionComplete, { success: false })))
      .toStrictEqual([{ kind: 'text', text: 'Compaction failed (unknown reason)' }])
  })
})
