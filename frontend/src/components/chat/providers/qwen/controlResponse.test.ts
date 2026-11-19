import type { ControlPrompt } from '../../model/controlPrompt'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import { describe, expect, it } from 'vitest'
import { qwenControlResponseSummary } from './controlResponse'

function record(request: Record<string, unknown> | undefined, result: Record<string, unknown>, prompt?: ControlPrompt): PersistedControlResponse {
  return {
    requestId: 'jsonrpc:5',
    claimToken: 'claim',
    request,
    response: { jsonrpc: '2.0', id: 5, result },
    ...(prompt
      ? { prompt }
      : {}),
  }
}

function selected(optionId: string, extra: Record<string, unknown> = {}) {
  return {
    outcome: { outcome: 'selected', optionId },
    ...extra,
  }
}

const PLAN = {
  method: 'session/request_permission',
  params: {
    options: [
      { optionId: 'restore_previous', name: 'Yes, restore previous mode (default)', kind: 'allow_once' },
      { optionId: 'proceed_always', name: 'Yes, and auto-accept edits', kind: 'allow_always' },
      { optionId: 'proceed_once', name: 'Yes, and manually approve edits', kind: 'allow_once' },
      { optionId: 'cancel', name: 'No, keep planning (esc)', kind: 'reject_once' },
    ],
    toolCall: {
      toolCallId: 'p',
      _meta: { toolName: 'exit_plan_mode' },
    },
  },
}

const QUESTION = {
  method: 'session/request_permission',
  params: {
    options: [
      { optionId: 'proceed_once', name: 'Submit', kind: 'allow_once' },
      { optionId: 'cancel', name: 'Cancel', kind: 'reject_once' },
    ],
    toolCall: {
      toolCallId: 'q',
      _meta: {
        toolName: 'ask_user_question',
        qwenInteractionKind: 'user_question',
        qwenQuestions: [
          {
            question: 'Color?',
            header: 'Color',
            options: [
              { label: 'Blue' },
            ],
          },
        ],
      },
    },
  },
}

const SHELL = {
  method: 'session/request_permission',
  params: {
    options: [
      { optionId: 'proceed_once', name: 'Allow', kind: 'allow_once' },
      { optionId: 'cancel', name: 'Reject', kind: 'reject_once' },
    ],
    toolCall: {
      toolCallId: 's',
      _meta: { toolName: 'run_shell_command' },
    },
  },
}

/** The prompts that the Worker records for the requests above. */
const PLAN_PROMPT: ControlPrompt = { kind: 'plan' }
const QUESTION_PROMPT: ControlPrompt = {
  kind: 'question',
  questions: [
    {
      question: 'Color?',
      header: 'Color',
      options: [
        { label: 'Blue' },
      ],
    },
  ],
}
const SHELL_PROMPT: ControlPrompt = {
  kind: 'permission',
  permission: { options: SHELL.params.options },
}

describe('qwenControlResponseSummary', () => {
  it('shows a plan decision in the words of the plan buttons', () => {
    expect(qwenControlResponseSummary(record(PLAN, selected('proceed_once'), PLAN_PROMPT))).toEqual({ kind: 'label', text: 'Approve' })
    expect(qwenControlResponseSummary(record(PLAN, selected('proceed_always'), PLAN_PROMPT))).toEqual({ kind: 'label', text: 'Approve' })
    expect(qwenControlResponseSummary(record(PLAN, selected('restore_previous'), PLAN_PROMPT))).toEqual({ kind: 'label', text: 'Approve' })
    expect(qwenControlResponseSummary(record(PLAN, selected('cancel'), PLAN_PROMPT))).toEqual({ kind: 'label', text: 'Reject' })
  })

  // The prompt states the kind of the request apart from the request itself, so a plan
  // approval whose request is gone still reads in the plan words.
  it('shows a plan decision in the plan words when the request is gone', () => {
    expect(qwenControlResponseSummary(record(undefined, selected('proceed_once'), PLAN_PROMPT))).toEqual({ kind: 'label', text: 'Approve' })
    expect(qwenControlResponseSummary(record(undefined, selected('cancel'), PLAN_PROMPT))).toEqual({ kind: 'label', text: 'Reject' })
  })

  // The kind comes from the prompt alone: a row that recorded none is no plan, and its
  // option reads in the request's own words.
  it('shows the option of a plan request whose row recorded no prompt', () => {
    expect(qwenControlResponseSummary(record(PLAN, selected('proceed_once')))).toEqual({ kind: 'label', text: 'Yes, and manually approve edits' })
  })

  it('reads an option the request does not list from its well-known kind', () => {
    const planWithoutOptions = {
      ...PLAN,
      params: { ...PLAN.params, options: [] },
    }
    expect(qwenControlResponseSummary(record(planWithoutOptions, selected('proceed_once'), PLAN_PROMPT))).toEqual({ kind: 'label', text: 'Approve' })
    expect(qwenControlResponseSummary(record(planWithoutOptions, selected('mystery'), PLAN_PROMPT))).toEqual({ kind: 'label', text: 'mystery' })
  })

  it('shows the answers of a question, and the option that dismissed one', () => {
    expect(qwenControlResponseSummary(record(QUESTION, selected('proceed_once', {
      answers: { 0: 'Blue' },
    }), QUESTION_PROMPT))).toEqual({ kind: 'label', text: 'Color: Blue' })
    expect(qwenControlResponseSummary(record(QUESTION, selected('cancel'), QUESTION_PROMPT))).toEqual({ kind: 'label', text: 'Cancel' })
  })

  it('shows the chosen option of an ordinary permission', () => {
    expect(qwenControlResponseSummary(record(SHELL, selected('proceed_once'), SHELL_PROMPT))).toEqual({ kind: 'label', text: 'Allow' })
  })

  // The prompt states a permission, so the reply reads as its chosen option whatever
  // other field it carries.
  it('shows the chosen option of a permission whose reply carries an answers field', () => {
    expect(qwenControlResponseSummary(record(SHELL, selected('proceed_once', {
      answers: { 0: 'Blue' },
    }), SHELL_PROMPT))).toEqual({ kind: 'label', text: 'Allow' })
  })

  // Without the request, the answers are numbered rather than headed.
  it('shows the answers of a question whose request is gone', () => {
    expect(qwenControlResponseSummary(record(undefined, selected('proceed_once', {
      answers: { 0: 'Blue', 1: 'Cache, Metrics' },
    }), QUESTION_PROMPT)))
      .toEqual({ kind: 'label', text: 'Question 1: Blue\nQuestion 2: Cache, Metrics' })
  })

  // Only Qwen's question reply carries the answers field, so the reply alone states
  // that it answered a question. A row that recorded no prompt would otherwise read
  // the submit option's kind, "Allow once", for a question that the reader answered.
  it('shows the answers of a question whose row recorded no prompt', () => {
    expect(qwenControlResponseSummary(record(QUESTION, selected('proceed_once', {
      answers: { 0: 'Blue' },
    })))).toEqual({ kind: 'label', text: 'Color: Blue' })
    expect(qwenControlResponseSummary(record(undefined, selected('proceed_once', {
      answers: { 0: 'Blue' },
    })))).toEqual({ kind: 'label', text: 'Question 1: Blue' })
  })

  it('shows the option of a reply with no answers whose request is gone', () => {
    expect(qwenControlResponseSummary(record(undefined, selected('cancel')))).toEqual({ kind: 'label', text: 'Reject' })
    expect(qwenControlResponseSummary(record(undefined, selected('proceed_once', { answers: {} })))).toEqual({ kind: 'label', text: 'Allow once' })
  })

  // A stop withdraws a plan approval with the protocol's cancel, which selects no
  // option, so the plan words cannot apply.
  it('shows a withdrawn plan approval as cancelled', () => {
    expect(qwenControlResponseSummary(record(PLAN, {
      outcome: { outcome: 'cancelled' },
    }, PLAN_PROMPT))).toEqual({ kind: 'label', text: 'Cancelled' })
  })

  it('shows a plan option that states no kind by its own name', () => {
    const planWithCustomOption = {
      ...PLAN,
      params: {
        ...PLAN.params,
        options: [
          { optionId: 'later', name: 'Decide later' },
        ],
      },
    }
    expect(qwenControlResponseSummary(record(planWithCustomOption, selected('later'), PLAN_PROMPT))).toEqual({ kind: 'label', text: 'Decide later' })
  })

  it('answers null for a response that holds no result', () => {
    expect(qwenControlResponseSummary({
      requestId: 'jsonrpc:5',
      claimToken: 'claim',
      request: PLAN,
      response: undefined,
      prompt: PLAN_PROMPT,
    })).toBeNull()
    expect(qwenControlResponseSummary({
      requestId: 'jsonrpc:5',
      claimToken: 'claim',
      request: QUESTION,
      response: { jsonrpc: '2.0', id: 5 },
      prompt: QUESTION_PROMPT,
    })).toBeNull()
  })
})
