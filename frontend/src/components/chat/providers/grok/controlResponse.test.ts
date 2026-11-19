import type { ControlPrompt } from '../../model/controlPrompt'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import { describe, expect, it } from 'vitest'
import { resolveControlResponseSummary } from '../../persistedControlResponse'
import { grokControlResponseSummary } from './controlResponse'

function record(request: Record<string, unknown> | undefined, result: Record<string, unknown> | undefined, prompt?: ControlPrompt): PersistedControlResponse {
  return {
    requestId: 'jsonrpc:1',
    claimToken: 'claim',
    request,
    response: result === undefined
      ? undefined
      : { jsonrpc: '2.0', id: 1, result },
    ...(prompt
      ? { prompt }
      : {}),
  }
}

const PLAN = {
  jsonrpc: '2.0',
  id: 1,
  method: '_x.ai/exit_plan_mode',
  params: { toolCallId: 'c', planContent: '1. X' },
}
const QUESTION = {
  jsonrpc: '2.0',
  id: 1,
  method: '_x.ai/ask_user_question',
  params: {
    questions: [
      {
        question: 'Pick',
        options: [
          { label: 'A' },
          { label: 'B' },
        ],
      },
    ],
  },
}
const ELICIT = {
  jsonrpc: '2.0',
  id: 1,
  method: '_x.ai/mcp/elicit',
  params: {
    serverName: 'docs',
    mode: 'form',
    message: 'Choose',
    requestedSchema: {
      type: 'object',
      properties: {
        size: { type: 'string', title: 'Size' },
      },
    },
  },
}
/** The form prompt that the Worker records for {@link ELICIT}. */
const ELICIT_PROMPT: ControlPrompt = {
  kind: 'elicitation',
  elicitation: {
    mode: 'form',
    message: 'Choose',
    server: 'docs',
    schema: ELICIT.params.requestedSchema,
  },
}
const TRUST = {
  jsonrpc: '2.0',
  id: 1,
  method: '_x.ai/folder_trust/request',
  params: {
    cwd: '/w',
    workspace: '/w',
    configKinds: ['mcp'],
  },
}
const PERMISSION = {
  jsonrpc: '2.0',
  id: 1,
  method: 'session/request_permission',
  params: {
    toolCall: { toolCallId: 'c' },
    options: [
      { optionId: 'allow-once', name: 'Yes, proceed', kind: 'allow_once' },
      { optionId: 'reject-once', name: 'No, and tell Grok what to do differently', kind: 'reject_once' },
    ],
  },
}

/** The prompt that the Worker reads from {@link PLAN}. */
const PLAN_PROMPT: ControlPrompt = { kind: 'plan', text: '1. X' }

/** The prompt that the Worker reads from {@link QUESTION}. */
const QUESTION_PROMPT: ControlPrompt = {
  kind: 'question',
  questions: [
    {
      question: 'Pick',
      options: [
        { label: 'A' },
        { label: 'B' },
      ],
    },
  ],
  preservesSelectionNotes: true,
}

/** The prompt that the Worker reads from {@link TRUST} (`grokFolderTrustPermission` in Go). */
const TRUST_PROMPT: ControlPrompt = {
  kind: 'permission',
  permission: {
    title: 'Trust the workspace /w',
    reason: 'The repository holds its own Grok Build configuration (mcp). Grok Build loads it only from a workspace you trust, and it keeps your answer for the workspace.',
    options: [
      { optionId: 'trust', kind: 'allow_once', name: 'Trust this workspace' },
      { optionId: 'reject', kind: 'reject_once', name: 'Do not trust' },
    ],
  },
}

/** The prompt that the Worker reads from {@link PERMISSION}. */
const PERMISSION_PROMPT: ControlPrompt = {
  kind: 'permission',
  permission: {
    options: [
      { optionId: 'allow-once', kind: 'allow_once', name: 'Yes, proceed' },
      { optionId: 'reject-once', kind: 'reject_once', name: 'No, and tell Grok what to do differently' },
    ],
  },
}

describe('grokControlResponseSummary', () => {
  it('shows a plan decision in the words of its buttons', () => {
    expect(grokControlResponseSummary(record(PLAN, { outcome: 'approved' }, PLAN_PROMPT))).toEqual({ kind: 'label', text: 'Approve' })
    expect(grokControlResponseSummary(record(PLAN, { outcome: 'cancelled' }, PLAN_PROMPT))).toEqual({ kind: 'label', text: 'Reject' })
    expect(grokControlResponseSummary(record(PLAN, { outcome: 'cancelled', feedback: 'Split step 2.' }, PLAN_PROMPT))).toEqual({ kind: 'feedback', message: 'Split step 2.' })
    expect(grokControlResponseSummary(record(PLAN, { outcome: 'something-new' }, PLAN_PROMPT))).toBeNull()
  })

  it('shows the button word for a rejected plan whose feedback is only whitespace', () => {
    expect(grokControlResponseSummary(record(PLAN, { outcome: 'cancelled', feedback: '   ' }, PLAN_PROMPT))).toEqual({ kind: 'label', text: 'Reject' })
  })

  it('shows the answers of a question', () => {
    expect(grokControlResponseSummary(record(QUESTION, {
      outcome: 'accepted',
      answers: {
        Pick: ['B'],
      },
    }, QUESTION_PROMPT))).toEqual({ kind: 'label', text: 'Pick: B' })
    expect(grokControlResponseSummary(record(QUESTION, { outcome: 'cancelled' }, QUESTION_PROMPT))).toEqual({ kind: 'label', text: 'Cancel' })
    expect(grokControlResponseSummary(record(QUESTION, { outcome: 'accepted', answers: {} }, QUESTION_PROMPT))).toBeNull()
    expect(grokControlResponseSummary(record(QUESTION, { outcome: 'something-new' }, QUESTION_PROMPT))).toBeNull()
  })

  // The questions and their order come from the prompt that the banner drew, not from
  // the stored request.
  it('reads the questions of the recorded prompt, not of the native request', () => {
    const prompt: ControlPrompt = {
      kind: 'question',
      questions: [
        { question: 'Second', options: [] },
        { question: 'Pick', options: [] },
      ],
    }
    expect(grokControlResponseSummary(record(QUESTION, {
      outcome: 'accepted',
      answers: {
        Pick: ['B'],
        Second: ['Yes'],
        Unasked: ['X'],
      },
    }, prompt))).toEqual({ kind: 'label', text: 'Second: Yes\nPick: B' })
    expect(grokControlResponseSummary(record(undefined, {
      outcome: 'accepted',
      answers: {
        Pick: ['B'],
      },
    }, QUESTION_PROMPT))).toEqual({ kind: 'label', text: 'Pick: B' })
  })

  it('shows an MCP form answer through the shared form display', () => {
    expect(grokControlResponseSummary(record(ELICIT, {
      outcome: 'accept',
      content: { size: 'Large' },
    }, ELICIT_PROMPT))).toEqual({ kind: 'label', text: 'Approved\nSize: Large' })
    expect(grokControlResponseSummary(record(ELICIT, { outcome: 'decline' }, ELICIT_PROMPT))).toEqual({ kind: 'label', text: 'Rejected' })
    expect(grokControlResponseSummary(record(ELICIT, { outcome: 'cancel' }, ELICIT_PROMPT))).toEqual({ kind: 'label', text: 'Cancelled' })
    expect(grokControlResponseSummary(record(ELICIT, { outcome: 'other' }, ELICIT_PROMPT))).toBeNull()
  })

  // The form's field titles come from the prompt that the Worker recorded, not from
  // Grok's own request, so a row that recorded no form prompt has no form to read.
  it('states nothing for an MCP form answer whose row recorded no form prompt', () => {
    expect(grokControlResponseSummary(record(ELICIT, {
      outcome: 'accept',
      content: { size: 'Large' },
    }))).toBeNull()
    expect(grokControlResponseSummary(record(ELICIT, { outcome: 'decline' }, { kind: 'plan' }))).toBeNull()
  })

  it('shows the folder-trust answer in the words of its button', () => {
    expect(grokControlResponseSummary(record(TRUST, { outcome: 'trust' }, TRUST_PROMPT))).toEqual({ kind: 'label', text: 'Trust this workspace' })
    expect(grokControlResponseSummary(record(TRUST, { outcome: 'reject' }, TRUST_PROMPT))).toEqual({ kind: 'label', text: 'Do not trust' })
    expect(grokControlResponseSummary(record(TRUST, { outcome: 'x' }, TRUST_PROMPT))).toBeNull()
  })

  // The words come from the options of the prompt that the banner drew.
  it('shows the folder-trust answer in the words of the recorded prompt', () => {
    const prompt: ControlPrompt = {
      kind: 'permission',
      permission: {
        options: [
          { optionId: 'trust', kind: 'allow_once', name: 'Load its configuration' },
          { optionId: 'reject', kind: 'reject_once' },
        ],
      },
    }
    expect(grokControlResponseSummary(record(TRUST, { outcome: 'trust' }, prompt))).toEqual({ kind: 'label', text: 'Load its configuration' })
    expect(grokControlResponseSummary(record(TRUST, { outcome: 'reject' }, prompt))).toEqual({ kind: 'label', text: 'Reject' })
    expect(grokControlResponseSummary(record(undefined, { outcome: 'trust' }, prompt))).toEqual({ kind: 'label', text: 'Load its configuration' })
  })

  // The reply word is wire data, so one that spells an `Object.prototype` member must
  // read as no answer rather than as that member.
  it.each(['constructor', 'toString', '__proto__'])('shows no folder-trust answer for the reply word %s', (outcome) => {
    expect(grokControlResponseSummary(record(TRUST, { outcome }, TRUST_PROMPT))).toBeNull()
  })

  it('shows the reason a rejected permission carried', () => {
    expect(grokControlResponseSummary(record(PERMISSION, {
      outcome: { outcome: 'selected', optionId: 'reject-once' },
      _meta: { followup_message: ' Use pnpm ' },
    }, PERMISSION_PROMPT)))
      .toEqual({ kind: 'feedback', message: 'Use pnpm' })
  })

  it('shows the chosen option of a permission', () => {
    expect(grokControlResponseSummary(record(PERMISSION, {
      outcome: { outcome: 'selected', optionId: 'allow-once' },
    }, PERMISSION_PROMPT))).toEqual({ kind: 'label', text: 'Yes, proceed' })
  })

  // A reason of only whitespace is no reason, so the row states the button instead.
  it('shows the chosen option when the follow-up message is only whitespace', () => {
    expect(grokControlResponseSummary(record(PERMISSION, {
      outcome: { outcome: 'selected', optionId: 'reject-once' },
      _meta: { followup_message: '  ' },
    }, PERMISSION_PROMPT)))
      .toEqual({ kind: 'label', text: 'No, and tell Grok what to do differently' })
  })

  it('falls back to the shared display when the reply is no result', () => {
    expect(grokControlResponseSummary(record(PLAN, undefined, PLAN_PROMPT))).toBeNull()
  })

  // The prompt states which request the answer belongs to. A row with none reads as
  // nothing here, and the shared chokepoint shows its generic label.
  it('reads nothing from a row that records no prompt', () => {
    const cr = record(TRUST, { outcome: 'trust' })
    expect(grokControlResponseSummary(cr)).toBeNull()
    expect(grokControlResponseSummary(record(PLAN, { outcome: 'approved' }))).toBeNull()
    expect(resolveControlResponseSummary(cr, grokControlResponseSummary)).toEqual({ kind: 'label', text: 'Responded' })
  })
})
