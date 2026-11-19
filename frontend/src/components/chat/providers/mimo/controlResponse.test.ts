import type { ControlPrompt } from '../../model/controlPrompt'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import { describe, expect, it } from 'vitest'
import { buildAllowResponse, buildDenyResponse } from '~/test-support/controlResponses'
import { resolveControlResponseSummary } from '../../persistedControlResponse'
import { mimoControlResponseSummary } from './controlResponse'

const PERMISSION = {
  type: 'permission.asked',
  properties: { id: 'per_1', permission: 'bash' },
  request: { tool_name: 'bash' },
}
const QUESTION = {
  type: 'question.asked',
  properties: {
    id: 'que_1',
    questions: [
      { question: 'Which database?', header: 'Database' },
      { question: 'Why?' },
    ],
  },
  request: { tool_name: 'question' },
}
const PLAN = {
  type: 'question.asked',
  properties: {
    id: 'que_2',
    questions: [
      { key: 'plan_exit' },
    ],
  },
  request: { tool_name: 'plan_exit' },
  plan: '# Plan',
}

/** The permission prompt that the Worker reads from {@link PERMISSION} (`mimoPermissionPrompt` in Go). */
const PERMISSION_PROMPT: ControlPrompt = {
  kind: 'permission',
  permission: {
    title: 'bash',
    options: [
      { optionId: 'once', kind: 'allow_once', name: 'Allow once' },
      { optionId: 'always', kind: 'allow_always', name: 'Always allow' },
      { optionId: 'reject', kind: 'reject_once', name: 'Reject' },
    ],
  },
}

const PROMPTS = new Map<Record<string, unknown> | undefined, ControlPrompt>([
  [PERMISSION, PERMISSION_PROMPT],
  [
    QUESTION,
    {
      kind: 'question',
      questions: [
        { question: 'Which database?', header: 'Database', options: [] },
        { question: 'Why?', options: [] },
      ],
    },
  ],
  [
    PLAN,
    { kind: 'plan', text: '# Plan' },
  ],
])

/**
 * A saved answer, with the prompt that the Worker records for the request: by default
 * the prompt of the request fixture below, or none for any other request. `null`
 * states a row that recorded no prompt.
 */
function saved(request: Record<string, unknown> | undefined, response: Record<string, unknown> | undefined, prompt: ControlPrompt | null = PROMPTS.get(request) ?? null): PersistedControlResponse {
  return {
    requestId: 'r',
    claimToken: 't',
    request,
    response,
    ...(prompt
      ? { prompt }
      : {}),
  }
}

describe('mimoControlResponseSummary', () => {
  it('states the permission option the reader chose', () => {
    const selected = (optionId: string) => ({
      jsonrpc: '2.0',
      id: 'r',
      result: {
        outcome: { outcome: 'selected', optionId },
      },
    })
    expect(mimoControlResponseSummary(saved(PERMISSION, selected('once')))).toEqual({ kind: 'label', text: 'Allow once' })
    expect(mimoControlResponseSummary(saved(PERMISSION, selected('always')))).toEqual({ kind: 'label', text: 'Always allow' })
    expect(mimoControlResponseSummary(saved(PERMISSION, selected('reject')))).toEqual({ kind: 'label', text: 'Reject' })
    expect(mimoControlResponseSummary(saved(PERMISSION, {
      result: {
        outcome: { outcome: 'cancelled' },
      },
    }))).toEqual({ kind: 'label', text: 'Deny' })
    expect(mimoControlResponseSummary(saved(PERMISSION, selected('forever')))).toBeNull()
    expect(mimoControlResponseSummary(saved(PERMISSION, selected('toString')))).toBeNull()
  })

  // The words come from the options of the prompt that the banner drew, so a prompt
  // whose options state other words reads those.
  it('states the chosen option in the words of the recorded prompt', () => {
    const selected = (optionId: string) => ({
      result: {
        outcome: { outcome: 'selected', optionId },
      },
    })
    const prompt: ControlPrompt = {
      kind: 'permission',
      permission: {
        title: 'bash',
        options: [
          { optionId: 'once', kind: 'allow_once', name: 'Run it this time' },
          { optionId: 'reject', kind: 'reject_once' },
        ],
      },
    }
    expect(mimoControlResponseSummary(saved(PERMISSION, selected('once'), prompt))).toEqual({ kind: 'label', text: 'Run it this time' })
    expect(mimoControlResponseSummary(saved(PERMISSION, selected('reject'), prompt))).toEqual({ kind: 'label', text: 'Reject' })
    // The Worker leaves out Always allow for a request that saves no pattern.
    expect(mimoControlResponseSummary(saved(PERMISSION, selected('always'), prompt))).toBeNull()
  })

  it('states each answer under its question', () => {
    expect(mimoControlResponseSummary(saved(QUESTION, {
      result: {
        answers: [
          ['SQLite'],
          ['speed'],
        ],
      },
    }))).toEqual({
      kind: 'label',
      text: 'Database: SQLite\nWhy?: speed',
    })
  })

  it('states a dismissed question', () => {
    expect(mimoControlResponseSummary(saved(QUESTION, {
      result: { rejected: true },
    }))).toEqual({ kind: 'label', text: 'Dismissed' })
  })

  // The Worker answers a plan with the shared allow/deny envelope. The envelope does not
  // state which control it answers, and the recorded prompt does: a plan takes the plan
  // words.
  it('states a plan answer in the plan words', () => {
    expect(mimoControlResponseSummary(saved(PLAN, buildAllowResponse('r', {})))).toEqual({ kind: 'label', text: 'Approve' })
    expect(mimoControlResponseSummary(saved(PLAN, buildDenyResponse('r')))).toEqual({ kind: 'label', text: 'Reject' })
    expect(mimoControlResponseSummary(saved(PLAN, buildDenyResponse('r', 'Split the migration')))).toEqual({ kind: 'feedback', message: 'Split the migration' })
    expect(mimoControlResponseSummary(saved(PLAN, {
      result: { rejected: true },
    }))).toEqual({ kind: 'label', text: 'Reject' })
  })

  // The kind of the request comes from the prompt alone, so a plan whose row recorded
  // no prompt reads in the permission words.
  it('states a plan answer whose row recorded no prompt in the permission words', () => {
    expect(mimoControlResponseSummary(saved(PLAN, buildAllowResponse('r', {}), null))).toEqual({ kind: 'label', text: 'Allow' })
    expect(mimoControlResponseSummary(saved(PLAN, buildDenyResponse('r'), null))).toEqual({ kind: 'label', text: 'Deny' })
  })

  // The Worker answers a permission that the reader refused with feedback through the
  // same envelope.
  it('states a permission answered through the envelope in the permission words', () => {
    expect(mimoControlResponseSummary(saved(PERMISSION, buildDenyResponse('r', 'Keep the file')))).toEqual({ kind: 'feedback', message: 'Keep the file' })
    expect(mimoControlResponseSummary(saved(PERMISSION, buildDenyResponse('r')))).toEqual({ kind: 'label', text: 'Deny' })
  })

  it('reads no answer that states no result', () => {
    expect(mimoControlResponseSummary(saved(PERMISSION, undefined))).toBeNull()
    expect(mimoControlResponseSummary(saved(PERMISSION, { jsonrpc: '2.0', id: 'r' }))).toBeNull()
  })

  // A permission answer with no chosen option is a refusal, whatever else it states.
  it('states a permission result with no outcome as a denial', () => {
    expect(mimoControlResponseSummary(saved(PERMISSION, { result: {} }))).toEqual({ kind: 'label', text: 'Deny' })
    expect(mimoControlResponseSummary(saved(PERMISSION, {
      result: {
        outcome: { outcome: 'selected' },
      },
    }))).toBeNull()
  })

  // The prompt lives in the answer row's own metadata, so a response whose request did
  // not survive still reads as the option.
  it('states a chosen option when the request is absent', () => {
    expect(mimoControlResponseSummary(saved(undefined, {
      result: {
        outcome: { outcome: 'selected', optionId: 'always' },
      },
    }, PERMISSION_PROMPT))).toEqual({ kind: 'label', text: 'Always allow' })
  })

  // The prompt states which request a result answers. A result whose row records none
  // reads as nothing here, and the shared chokepoint shows its generic label.
  it('reads nothing from a result whose row records no prompt', () => {
    const row = saved(PERMISSION, {
      result: {
        outcome: { outcome: 'selected', optionId: 'once' },
      },
    }, null)
    expect(mimoControlResponseSummary(row)).toBeNull()
    expect(mimoControlResponseSummary(saved(QUESTION, {
      result: {
        answers: [
          ['SQLite'],
        ],
      },
    }, null))).toBeNull()
    expect(resolveControlResponseSummary(row, mimoControlResponseSummary)).toEqual({ kind: 'label', text: 'Responded' })
  })

  it('labels an answer by its question when the question has no header, and by its position when it has neither', () => {
    const request = {
      type: 'question.asked',
      properties: {
        questions: [
          { question: 'Why?' },
          {},
        ],
      },
      request: { tool_name: 'question' },
    }
    const prompt: ControlPrompt = {
      kind: 'question',
      questions: [
        { question: 'Why?', options: [] },
        { question: '', options: [] },
      ],
    }
    expect(mimoControlResponseSummary(saved(request, {
      result: {
        answers: [
          ['speed'],
          ['size'],
        ],
      },
    }, prompt))).toEqual({ kind: 'label', text: 'Why?: speed\nQuestion 2: size' })
  })

  // The questions come from the prompt that the banner drew, not from the stored
  // request.
  it('labels each answer with the questions of the recorded prompt, not of the native request', () => {
    const prompt: ControlPrompt = {
      kind: 'question',
      questions: [
        { question: 'Which engine?', header: 'Engine', options: [] },
      ],
    }
    expect(mimoControlResponseSummary(saved(QUESTION, {
      result: {
        answers: [
          ['SQLite'],
        ],
      },
    }, prompt))).toEqual({ kind: 'label', text: 'Engine: SQLite' })
  })

  // An answer beyond the questions the request kept still reaches the row, under its
  // position.
  it('labels an answer beyond the stated questions by its position', () => {
    expect(mimoControlResponseSummary(saved(QUESTION, {
      result: {
        answers: [
          ['SQLite'],
          ['speed'],
          ['extra'],
        ],
      },
    }))).toEqual({
      kind: 'label',
      text: 'Database: SQLite\nWhy?: speed\nQuestion 3: extra',
    })
    expect(mimoControlResponseSummary(saved(undefined, {
      result: {
        answers: [
          ['A'],
        ],
      },
    }, { kind: 'question', questions: [] }))).toEqual({ kind: 'label', text: 'Question 1: A' })
  })

  it('trims each choice and skips an empty one, and skips a question with no answer', () => {
    expect(mimoControlResponseSummary(saved(QUESTION, {
      result: {
        answers: [
          ['  SQLite ', '', 'Postgres'],
          [],
        ],
      },
    }))).toEqual({ kind: 'label', text: 'Database: SQLite, Postgres' })
  })

  it.each([
    [
      'no answer at all',
      { answers: [] },
    ],
    [
      'only empty answers',
      {
        answers: [
          [],
          ['  '],
        ],
      },
    ],
    [
      'answers that are not a list',
      { answers: 'SQLite' },
    ],
    ['no answers field', {}],
  ])('reads no answer from a question result with %s', (_name, result) => {
    expect(mimoControlResponseSummary(saved(QUESTION, { result }))).toBeNull()
  })

  // Only a rejection of a plan states a word of its own. Any other result names no
  // choice the plan buttons offer.
  it('states nothing for a plan result that is not a rejection', () => {
    expect(mimoControlResponseSummary(saved(PLAN, {
      result: {
        answers: [
          ['Yes'],
        ],
      },
    }))).toBeNull()
    expect(mimoControlResponseSummary(saved(PLAN, {
      result: { rejected: false },
    }))).toBeNull()
  })
})
