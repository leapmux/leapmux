import type { ControlPrompt } from '../../model/controlPrompt'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import type { CodexDecision } from './controlResponse'
import { describe, expect, it } from 'vitest'
import { resolveControlResponseSummary } from '../../persistedControlResponse'
import { codexControlResponseSummary, codexDecisionLabel } from './controlResponse'

/** One saved row, with the prompt that the Worker recorded beside it, if any. */
function cr(request: Record<string, unknown> | undefined, response: Record<string, unknown> | undefined, prompt?: ControlPrompt): PersistedControlResponse {
  return {
    claimToken: 'claim-1',
    requestId: '7',
    request,
    response,
    ...(prompt
      ? { prompt }
      : {}),
  }
}

const APPROVAL_REQUEST = { method: 'item/commandExecution/requestApproval' }

/** The prompt that the Worker reads from {@link APPROVAL_REQUEST}, which offers no decision list. */
const APPROVAL_PROMPT: ControlPrompt = {
  kind: 'permission',
  permission: {
    title: 'Command Execution',
    options: [
      { optionId: 'accept', kind: 'allow_once', name: 'Allow' },
      { optionId: 'cancel', kind: 'reject_once', name: 'Cancel' },
    ],
  },
}

/** LeapMux's own plan-mode prompt, which the Worker reads as a plan with no text. */
const PLAN_REQUEST = {
  request: { tool_name: 'CodexPlanModePrompt', input: {} },
}

/** The prompt that the Worker reads from {@link PLAN_REQUEST}. */
const PLAN_PROMPT: ControlPrompt = { kind: 'plan' }

function decision(d: unknown): Record<string, unknown> {
  return {
    jsonrpc: '2.0',
    id: 7,
    result: { decision: d },
  }
}

describe('codexDecisionLabel', () => {
  it('maps the known string decisions', () => {
    expect(codexDecisionLabel('accept')).toBe('Allow')
    expect(codexDecisionLabel('acceptForSession')).toBe('Allow for Session')
    expect(codexDecisionLabel('decline')).toBe('Deny')
    expect(codexDecisionLabel('cancel')).toBe('Cancel')
  })

  it('maps exact amendment objects and refuses unknown variants', () => {
    expect(codexDecisionLabel('somethingElse')).toBe('Unknown decision')
    expect(codexDecisionLabel({
      acceptWithExecpolicyAmendment: {
        execpolicy_amendment: ['npm', 'test'],
      },
    })).toBe('Allow & Remember')
    expect(codexDecisionLabel({
      applyNetworkPolicyAmendment: {
        network_policy_amendment: { host: 'example.com', action: 'allow' },
      },
    })).toBe('Allow Host & Remember')
    expect(codexDecisionLabel({ other: 1 })).toBe('Unknown decision')
  })

  it('is total: a malformed decision is not actionable', () => {
    // Persisted provider data can contain a malformed decision.
    expect(() => codexDecisionLabel(null as unknown as CodexDecision)).not.toThrow()
    expect(codexDecisionLabel(null as unknown as CodexDecision)).toBe('Unknown decision')
    expect(codexDecisionLabel(3 as unknown as CodexDecision)).toBe('Unknown decision')
  })
})

/** The neutral envelope that the Worker stores for an answer that LeapMux owns. */
function neutral(inner: Record<string, unknown>): Record<string, unknown> {
  return {
    type: 'control_response',
    response: { request_id: '7', response: inner },
  }
}

describe('codexControlResponseSummary', () => {
  it('labels string decisions', () => {
    expect(codexControlResponseSummary(cr(APPROVAL_REQUEST, decision('accept'), APPROVAL_PROMPT))).toEqual({ kind: 'label', text: 'Allow' })
    expect(codexControlResponseSummary(cr(APPROVAL_REQUEST, decision('decline'), APPROVAL_PROMPT))).toEqual({ kind: 'label', text: 'Deny' })
    expect(codexControlResponseSummary(cr(APPROVAL_REQUEST, decision(' cancel '), APPROVAL_PROMPT))).toEqual({ kind: 'label', text: 'Cancel' })
  })

  it('labels amendment-object decisions', () => {
    expect(codexControlResponseSummary(cr(APPROVAL_REQUEST, decision({
      acceptWithExecpolicyAmendment: {
        execpolicy_amendment: ['touch'],
      },
    }), APPROVAL_PROMPT)))
      .toEqual({ kind: 'label', text: 'Allow & Remember' })
    expect(codexControlResponseSummary(cr(APPROVAL_REQUEST, decision({
      applyNetworkPolicyAmendment: {
        network_policy_amendment: { host: 'example.com', action: 'allow' },
      },
    }), APPROVAL_PROMPT)))
      .toEqual({ kind: 'label', text: 'Allow Host & Remember' })
  })

  it('returns null for a missing/empty decision (caller degrades)', () => {
    expect(codexControlResponseSummary(cr(APPROVAL_REQUEST, decision(null), APPROVAL_PROMPT))).toBeNull()
    expect(codexControlResponseSummary(cr(APPROVAL_REQUEST, decision({}), APPROVAL_PROMPT))).toBeNull()
    expect(codexControlResponseSummary(cr(APPROVAL_REQUEST, { result: {} }, APPROVAL_PROMPT))).toBeNull()
  })

  // The decision states its own words, so a row with no prompt still reads it.
  it('labels a decision whose row records no prompt', () => {
    expect(codexControlResponseSummary(cr(APPROVAL_REQUEST, decision('accept')))).toEqual({ kind: 'label', text: 'Allow' })
  })

  // The plan-mode prompt answers in the neutral envelope, which does not state the
  // control that it answers. The recorded prompt does, so the plan reads in the words
  // of its own buttons.
  it('reads a plan answer in the plan words, and a refusal with its typed reason', () => {
    expect(codexControlResponseSummary(cr(PLAN_REQUEST, neutral({ behavior: 'allow', updatedInput: {} }), PLAN_PROMPT))).toEqual({ kind: 'label', text: 'Approve' })
    expect(codexControlResponseSummary(cr(PLAN_REQUEST, neutral({ behavior: 'deny', message: 'Rejected by user.' }), PLAN_PROMPT))).toEqual({ kind: 'label', text: 'Reject' })
    expect(codexControlResponseSummary(cr(PLAN_REQUEST, neutral({ behavior: 'deny', message: 'Add tests first.' }), PLAN_PROMPT))).toEqual({ kind: 'feedback', message: 'Add tests first.' })
  })

  // A row with no prompt states no control, so the envelope reads in the permission
  // words, as the shared fallback reads it.
  it('reads a neutral answer whose row records no prompt in the permission words', () => {
    expect(codexControlResponseSummary(cr(PLAN_REQUEST, neutral({ behavior: 'allow' })))).toEqual({ kind: 'label', text: 'Allow' })
    expect(codexControlResponseSummary(cr(PLAN_REQUEST, neutral({ behavior: 'deny', message: 'Rejected by user.' })))).toEqual({ kind: 'label', text: 'Deny' })
    expect(codexControlResponseSummary(cr(PLAN_REQUEST, neutral({ behavior: 'deny', message: 'Add tests first.' })))).toEqual({ kind: 'feedback', message: 'Add tests first.' })
  })

  it('reads nothing that the shared fallback cannot read either', () => {
    const row = cr(APPROVAL_REQUEST, { jsonrpc: '2.0', id: 7 })
    expect(codexControlResponseSummary(row)).toBeNull()
    expect(resolveControlResponseSummary(row, codexControlResponseSummary)).toEqual({ kind: 'label', text: 'Responded' })
  })

  // A LeapMux-owned async question answers in the neutral envelope, and its
  // own display (the quoted title with the answer, or the skip) wins over the
  // decision paths below.
  it('reads the async question summary first', () => {
    const request = {
      request: {
        tool_name: 'CodexAsyncQuestion',
        input: { title: 'Which color should I use?' },
      },
    }
    const prompt: ControlPrompt = {
      kind: 'question',
      questions: [
        { question: 'Which color should I use?', options: [] },
      ],
      preservesSelectionNotes: true,
    }
    const allow = {
      type: 'control_response',
      response: {
        request_id: '7',
        response: { behavior: 'allow', answer: 'Red' },
      },
    }
    expect(codexControlResponseSummary(cr(request, allow, prompt))).toEqual({ kind: 'label', text: 'Which color should I use?: Red' })
    const deny = {
      type: 'control_response',
      response: {
        request_id: '7',
        response: { behavior: 'deny', message: 'Rejected by user.' },
      },
    }
    expect(codexControlResponseSummary(cr(request, deny, prompt))).toEqual({ kind: 'label', text: 'Skipped: Which color should I use?' })
  })

  describe('requestuserinput answers', () => {
    const request = {
      method: 'item/tool/requestUserInput',
      params: {
        questions: [
          { id: 'task', header: 'Task', question: 'Which task?' },
          { id: 'reason', header: 'Reason', question: 'Why?' },
        ],
      },
    }

    /** The prompt that the Worker reads from these questions (`providerkit.QuestionsFromWire` in Go). */
    function questionPrompt(...questions: Array<{ id?: string, header?: string, question: string }>): ControlPrompt {
      return {
        kind: 'question',
        questions: questions.map(question => ({ ...question, options: [] })),
        preservesSelectionNotes: true,
      }
    }

    const prompt = questionPrompt(
      { id: 'task', header: 'Task', question: 'Which task?' },
      { id: 'reason', header: 'Reason', question: 'Why?' },
    )

    const bothAnswered = {
      result: {
        answers: {
          task: {
            answers: ['Inspect'],
          },
          reason: {
            answers: ['Parity'],
          },
        },
      },
    }

    it('renders prompt-ordered header-labeled answer lines', () => {
      expect(codexControlResponseSummary(cr(request, bothAnswered, prompt))).toEqual({ kind: 'label', text: 'Task: Inspect\nReason: Parity' })
    })

    // The questions, their order and their headers come from the prompt that the
    // banner drew, not from the stored request.
    it('reads the questions from the recorded prompt, not from the native request', () => {
      const recorded = questionPrompt(
        { id: 'reason', header: 'Recorded reason', question: 'Why?' },
        { id: 'task', header: 'Recorded task', question: 'Which task?' },
      )
      expect(codexControlResponseSummary(cr(request, bothAnswered, recorded))).toEqual({ kind: 'label', text: 'Recorded reason: Parity\nRecorded task: Inspect' })
    })

    // The Worker keys an answer by the id, else the header, else the question's
    // place in the prompt.
    it('reads an answer that the Worker keyed by the header or by the position', () => {
      const keyed = questionPrompt(
        { header: 'Scope', question: 'How wide?' },
        { question: 'Anything else?' },
      )
      expect(codexControlResponseSummary(cr(request, {
        result: {
          answers: {
            Scope: {
              answers: ['Repository'],
            },
            q1: {
              answers: ['No'],
            },
          },
        },
      }, keyed))).toEqual({ kind: 'label', text: 'Scope: Repository\nq1: No' })
    })

    it('drops all-empty answers and joins multiple values', () => {
      const response = {
        result: {
          answers: {
            task: {
              answers: ['A', ' ', 'B'],
            },
            reason: {
              answers: ['', '  '],
            },
          },
        },
      }
      expect(codexControlResponseSummary(cr(request, response, prompt))).toEqual({ kind: 'label', text: 'Task: A, B' })
    })

    it('appends answer keys not in the prompt in sorted order', () => {
      const response = {
        result: {
          answers: {
            task: {
              answers: ['T'],
            },
            zebra: {
              answers: ['Z'],
            },
            alpha: {
              answers: ['A'],
            },
          },
        },
      }
      expect(codexControlResponseSummary(cr(request, response, questionPrompt({ id: 'task', header: 'Task', question: 'Which task?' })))).toEqual({ kind: 'label', text: 'Task: T\nalpha: A\nzebra: Z' })
    })

    it('reads an answer key that spells a prototype member as its own answer', () => {
      const response = JSON.parse('{"result":{"answers":{"toString":{"answers":["A"]},"__proto__":{"answers":["B"]}}}}') as Record<string, unknown>
      expect(codexControlResponseSummary(cr(request, response, questionPrompt({ id: 'toString', question: 'One?' }, { id: 'constructor', question: 'Two?' })))).toEqual({ kind: 'label', text: 'toString: A\n__proto__: B' })
    })

    it('returns null when there are no answers', () => {
      expect(codexControlResponseSummary(cr(request, {
        result: { answers: {} },
      }, prompt))).toBeNull()
    })

    it('falls through to the decision label when a requestUserInput is declined', () => {
      // A retained decline decision uses the shared denial label.
      // The answer parser falls through when the response has no answers.
      expect(codexControlResponseSummary(cr(request, decision('decline'), prompt))).toEqual({ kind: 'label', text: 'Deny' })
      expect(codexControlResponseSummary(cr(request, decision('cancel'), prompt))).toEqual({ kind: 'label', text: 'Cancel' })
    })

    // The only neutral answer to a question is the async question's, so a question
    // answered in the neutral envelope reads as that one.
    it('reads a question answered in the neutral envelope as the async question', () => {
      expect(codexControlResponseSummary(cr(request, neutral({ behavior: 'deny', message: 'Need more detail.' }), prompt))).toEqual({ kind: 'label', text: 'Skipped: Which task?' })
    })

    // The prompt lives in the answer row's own metadata, so a row whose request is gone
    // still reads its headers.
    it('renders request-gone answers under the headers of the recorded prompt', () => {
      expect(codexControlResponseSummary(cr(undefined, bothAnswered, prompt))).toEqual({ kind: 'label', text: 'Task: Inspect\nReason: Parity' })
    })

    it('renders the answers of a row that records no prompt, labeled by key', () => {
      // The answer VALUES live entirely in the response, so they still render -- labeled by
      // the answer key (sorted, no prompt order) rather than the missing question header,
      // instead of degrading to the generic "Responded" label.
      expect(codexControlResponseSummary(cr(undefined, bothAnswered))).toEqual({ kind: 'label', text: 'reason: Parity\ntask: Inspect' })
    })
  })
})
