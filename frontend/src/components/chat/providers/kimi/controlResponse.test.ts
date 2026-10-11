import type { PersistedControlResponse } from '../../persistedControlResponse'
import { describe, expect, it } from 'vitest'
import { KIMI_DISPLAY, KIMI_TOOL } from '~/generated/contracts/kimi-protocol'
import { kimiApprovalRequest, kimiQuestionRequest } from '~/test-support/kimiFixtures'
import { kimiControlResponseSummary } from './controlResponse'

/** A saved answer: the worker's envelope around the server's own body. */
function saved(request: Record<string, unknown>, native: Record<string, unknown>): PersistedControlResponse {
  return {
    requestId: 'r',
    claimToken: 't',
    request,
    response: { type: 'control_response', response: { subtype: 'success', request_id: 'r', response: native } },
  }
}

const COMMAND = kimiApprovalRequest(KIMI_TOOL.Bash, { kind: 'command', command: 'ls' })
const PLAN = kimiApprovalRequest(KIMI_TOOL.ExitPlanMode, { kind: KIMI_DISPLAY.PlanReview, plan: '# P' })
const GOAL = kimiApprovalRequest(KIMI_TOOL.CreateGoal, { kind: KIMI_DISPLAY.GoalStart, objective: 'Ship it' })

describe('kimiControlResponseSummary', () => {
  it('words a permission answer', () => {
    expect(kimiControlResponseSummary(saved(COMMAND, { decision: 'approved' }))).toStrictEqual({ kind: 'label', text: 'Allow' })
    expect(kimiControlResponseSummary(saved(COMMAND, { decision: 'approved', scope: 'session' }))).toStrictEqual({ kind: 'label', text: 'Allow for this session' })
    expect(kimiControlResponseSummary(saved(COMMAND, { decision: 'rejected' }))).toStrictEqual({ kind: 'label', text: 'Deny' })
    expect(kimiControlResponseSummary(saved(COMMAND, { decision: 'rejected', feedback: 'Use rg.' }))).toStrictEqual({ kind: 'feedback', message: 'Use rg.' })
    expect(kimiControlResponseSummary(saved(COMMAND, { decision: 'cancelled' }))).toStrictEqual({ kind: 'label', text: 'Cancelled' })
    expect(kimiControlResponseSummary(saved(COMMAND, { decision: 'maybe' }))).toBeNull()
  })

  it('words a plan answer', () => {
    expect(kimiControlResponseSummary(saved(PLAN, { decision: 'approved' }))).toStrictEqual({ kind: 'label', text: 'Approve' })
    expect(kimiControlResponseSummary(saved(PLAN, { decision: 'approved', selected_label: 'Option A' }))).toStrictEqual({ kind: 'label', text: 'Approve: Option A' })
    expect(kimiControlResponseSummary(saved(PLAN, { decision: 'rejected', selected_label: 'Reject and Exit' }))).toStrictEqual({ kind: 'label', text: 'Rejected and left plan mode' })
    expect(kimiControlResponseSummary(saved(PLAN, { decision: 'rejected', selected_label: 'Revise' }))).toStrictEqual({ kind: 'label', text: 'Requested revisions' })
    expect(kimiControlResponseSummary(saved(PLAN, { decision: 'rejected', feedback: 'Split step 1.' }))).toStrictEqual({ kind: 'feedback', message: 'Split step 1.' })
    expect(kimiControlResponseSummary(saved(PLAN, { decision: 'rejected' }))).toStrictEqual({ kind: 'label', text: 'Reject' })
    expect(kimiControlResponseSummary(saved(PLAN, {}))).toBeNull()
  })

  it('words the reason of a plan revision, and no reason for a refusal that leaves plan mode', () => {
    expect(kimiControlResponseSummary(saved(PLAN, { decision: 'rejected', selected_label: 'Revise', feedback: 'Split step 1.' }))).toStrictEqual({ kind: 'feedback', message: 'Split step 1.' })
    expect(kimiControlResponseSummary(saved(PLAN, { decision: 'rejected', selected_label: 'Reject and Exit', feedback: 'Stop.' }))).toStrictEqual({ kind: 'label', text: 'Rejected and left plan mode' })
  })

  it('words a cancelled answer whatever the request asked', () => {
    for (const request of [COMMAND, PLAN, GOAL])
      expect(kimiControlResponseSummary(saved(request, { decision: 'cancelled', selected_label: 'Revise' }))).toStrictEqual({ kind: 'label', text: 'Cancelled' })
  })

  it('reads a reason of only whitespace as no reason', () => {
    expect(kimiControlResponseSummary(saved(COMMAND, { decision: 'rejected', feedback: '   ' }))).toStrictEqual({ kind: 'label', text: 'Deny' })
    expect(kimiControlResponseSummary(saved(COMMAND, { decision: 'rejected', feedback: '  Use rg.  ' }))).toStrictEqual({ kind: 'feedback', message: 'Use rg.' })
  })

  it('words a goal start answer', () => {
    expect(kimiControlResponseSummary(saved(GOAL, { decision: 'approved', selected_label: 'auto' }))).toStrictEqual({ kind: 'label', text: 'Started the goal in Never Ask' })
    expect(kimiControlResponseSummary(saved(GOAL, { decision: 'approved', selected_label: 'yolo' }))).toStrictEqual({ kind: 'label', text: 'Started the goal in Ask When Needed' })
    expect(kimiControlResponseSummary(saved(GOAL, { decision: 'approved', selected_label: 'manual' }))).toStrictEqual({ kind: 'label', text: 'Started the goal in Always Ask' })
    expect(kimiControlResponseSummary(saved(GOAL, { decision: 'approved', selected_label: 'plan' }))).toStrictEqual({ kind: 'label', text: 'Started the goal' })
    expect(kimiControlResponseSummary(saved(GOAL, { decision: 'approved' }))).toStrictEqual({ kind: 'label', text: 'Started the goal' })
    expect(kimiControlResponseSummary(saved(GOAL, { decision: 'rejected' }))).toStrictEqual({ kind: 'label', text: 'Declined' })
    expect(kimiControlResponseSummary(saved(GOAL, { decision: 'rejected', feedback: 'Not now.' }))).toStrictEqual({ kind: 'feedback', message: 'Not now.' })
    expect(kimiControlResponseSummary(saved(GOAL, { decision: 'maybe' }))).toBeNull()
  })

  it('words a question answer by the words the ids stand for', () => {
    const request = kimiQuestionRequest([
      { id: 'q_0', question: 'Which color?', options: [{ id: 'opt_0_0', label: 'Red' }, { id: 'opt_0_1', label: 'Blue' }] },
      { id: 'q_1', question: 'Which sizes?', options: [{ id: 'opt_1_0', label: 'S' }, { id: 'opt_1_1', label: 'M' }] },
      { id: 'q_2', question: 'Anything else?', options: [] },
      { id: 'q_3', question: 'Skipped one?', options: [] },
    ])
    const answers = {
      q_0: { kind: 'single', option_id: 'opt_0_1' },
      q_1: { kind: 'multi_with_other', option_ids: ['opt_1_0', 'unknown'], other_text: 'XL' },
      q_2: { kind: 'other', text: 'No.' },
      q_3: { kind: 'skipped' },
      toString: { kind: 'single', option_id: 'x' },
    }
    expect(kimiControlResponseSummary(saved(request, { answers, method: 'click' }))).toStrictEqual({
      kind: 'label',
      text: 'Which color?: Blue\nWhich sizes?: S, unknown, XL\nAnything else?: No.\nSkipped one?: Skipped',
    })
    expect(kimiControlResponseSummary(saved(request, { dismiss: true }))).toStrictEqual({ kind: 'label', text: 'Dismissed' })
    expect(kimiControlResponseSummary(saved(request, { answers: {} }))).toStrictEqual({ kind: 'label', text: 'No answer' })
    expect(kimiControlResponseSummary(saved(request, {}))).toBeNull()
  })

  it('words a plain multiple choice, and a choice with empty text of its own', () => {
    const request = kimiQuestionRequest([{ id: 'q_0', question: 'Which sizes?', options: [{ id: 'opt_0_0', label: 'S' }, { id: 'opt_0_1', label: 'M' }] }])
    expect(kimiControlResponseSummary(saved(request, { answers: { q_0: { kind: 'multi', option_ids: ['opt_0_0', 'opt_0_1'] } } })))
      .toStrictEqual({ kind: 'label', text: 'Which sizes?: S, M' })
    expect(kimiControlResponseSummary(saved(request, { answers: { q_0: { kind: 'multi_with_other', option_ids: ['opt_0_1'], other_text: '' } } })))
      .toStrictEqual({ kind: 'label', text: 'Which sizes?: M' })
  })

  it('names a question by its header, then by its id, when it states no question text', () => {
    const request = kimiQuestionRequest([
      { id: 'q_0', question: '', header: 'Color', options: [] },
      { id: 'q_1', question: '', options: [] },
    ])
    const answers = { q_0: { kind: 'other', text: 'Green' }, q_1: { kind: 'skipped' } }
    expect(kimiControlResponseSummary(saved(request, { answers }))).toStrictEqual({ kind: 'label', text: 'Color: Green\nq_1: Skipped' })
  })

  it('skips an answer it cannot read, and states no answer when none remains', () => {
    const request = kimiQuestionRequest([
      { id: 'q_0', question: 'Q0', options: [] },
      { id: 'q_1', question: 'Q1', options: [] },
      { id: 'q_2', question: 'Q2', options: [] },
    ])
    const answers = { q_0: 'not an answer', q_1: { kind: 'unknown_kind' }, q_2: { kind: 'other', text: '' } }
    expect(kimiControlResponseSummary(saved(request, { answers }))).toStrictEqual({ kind: 'label', text: 'No answer' })
    expect(kimiControlResponseSummary(saved(request, { answers: 'not a map' }))).toBeNull()
  })

  it('reads nothing from a response it cannot place', () => {
    expect(kimiControlResponseSummary({ requestId: 'r', claimToken: 't', request: undefined, response: undefined })).toBeNull()
    expect(kimiControlResponseSummary(saved({ type: 'other' }, { decision: 'approved' }))).toBeNull()
  })
})
