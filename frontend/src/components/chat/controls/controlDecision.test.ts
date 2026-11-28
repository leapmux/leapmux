import type { ControlPrompt, PermissionOption } from '../model/controlPrompt'
import type { ControlQuestion } from '../model/question'
import type { ControlDecision } from '~/generated/proto/leapmux/v1/agent_control_pb'
import { describe, expect, it } from 'vitest'
import { MCP_ELICITATION_ACTION } from '~/generated/contracts/mcp-elicitation'
import { DialogAction, ElicitationAction } from '~/generated/proto/leapmux/v1/agent_control_pb'
import {
  approvePlanDecision,
  cancelQuestionDecision,
  composerDecision,
  dialogDecision,
  elicitationDecision,
  permissionDecision,
  planChoiceDecision,
  questionDecision,
  refusePlanDecision,
  typedWithOptions,
} from './controlDecision'
import { createControlAnswerState } from './types'

/** The kinds of a decision, and the value of each. */
type DecisionKind = NonNullable<ControlDecision['kind']['case']>
type DecisionValue<K extends DecisionKind> = Extract<ControlDecision['kind'], { case: K }> extends { value: infer V } ? V : never

/** The value of one decision of kind `kind`, or a failure for a decision of another kind. */
function valueOf<K extends DecisionKind>(decision: ControlDecision | undefined, kind: K): DecisionValue<K> {
  if (decision?.kind.case !== kind)
    throw new Error(`the decision is ${decision?.kind.case ?? 'absent'}, not ${kind}`)
  return (decision.kind as { value: unknown }).value as DecisionValue<K>
}

const APPROVAL = { permissionMode: 'acceptEdits', clearContext: true }

describe('permissionDecision', () => {
  it('chooses the option with no reason by default', () => {
    expect(valueOf(permissionDecision('allow_once'), 'permission')).toMatchObject({ optionId: 'allow_once', feedback: '' })
  })

  it('carries the reason of a refusal', () => {
    expect(valueOf(permissionDecision('reject_once', 'use the other file'), 'permission')).toMatchObject({ optionId: 'reject_once', feedback: 'use the other file' })
  })
})

describe('approvePlanDecision', () => {
  it('approves with the settings and no choice', () => {
    expect(valueOf(approvePlanDecision(APPROVAL), 'plan')).toMatchObject({
      approve: true,
      choiceId: '',
      permissionMode: 'acceptEdits',
      clearContext: true,
      feedback: '',
    })
  })

  it('approves with one of the plan\'s approving choices', () => {
    expect(valueOf(approvePlanDecision({ permissionMode: '', clearContext: false }, 'yes-auto'), 'plan')).toMatchObject({
      approve: true,
      choiceId: 'yes-auto',
      permissionMode: '',
      clearContext: false,
    })
  })
})

describe('refusePlanDecision', () => {
  it('refuses with what to change and no settings', () => {
    expect(valueOf(refusePlanDecision('split the migration'), 'plan')).toMatchObject({
      approve: false,
      feedback: 'split the migration',
      choiceId: '',
      permissionMode: '',
      clearContext: false,
    })
  })

  it('refuses with no text', () => {
    expect(valueOf(refusePlanDecision(), 'plan')).toMatchObject({ approve: false, feedback: '' })
  })
})

describe('planChoiceDecision', () => {
  it('approves through an approving choice with the approval settings', () => {
    expect(valueOf(planChoiceDecision({ id: 'yes', label: 'Yes', effect: 'approve' }, APPROVAL), 'plan')).toMatchObject({
      approve: true,
      choiceId: 'yes',
      permissionMode: 'acceptEdits',
      clearContext: true,
    })
  })

  it.each(['refuse', 'other'] as const)('sends a choice of the %s effect with no settings', (effect) => {
    expect(valueOf(planChoiceDecision({ id: 'later', label: 'Later', effect }, APPROVAL), 'plan')).toMatchObject({
      approve: false,
      choiceId: 'later',
      permissionMode: '',
      clearContext: false,
      feedback: '',
    })
  })
})

describe('questionDecision', () => {
  const questions: ControlQuestion[] = [
    {
      question: 'Which colors?',
      multiSelect: true,
      options: [
        { label: 'Red' },
        { label: 'Blue' },
      ],
    },
    {
      question: 'Which size?',
      options: [
        { label: 'S' },
        { label: 'M' },
      ],
    },
    { question: 'Anything else?', options: [] },
  ]

  it('answers every question in order, with an empty answer where the reader gave none', () => {
    const state = createControlAnswerState({
      selections: {
        1: ['M'],
      },
    })
    expect(valueOf(questionDecision(state, { questions }), 'question').answers.map(({ selected, customText }) => ({ selected, customText }))).toEqual([
      { selected: [], customText: '' },
      {
        selected: ['M'],
        customText: '',
      },
      { selected: [], customText: '' },
    ])
  })

  it('keeps both the chosen options and the typed text of a multiple choice', () => {
    const state = createControlAnswerState({
      selections: {
        0: ['Red', 'Blue'],
      },
      customTexts: { 0: 'Green' },
    })
    const answer = valueOf(questionDecision(state, { questions }), 'question').answers[0]
    expect(answer?.selected).toEqual(['Red', 'Blue'])
    expect(answer?.customText).toBe('Green')
  })

  it('drops the typed text beside the selection of a single choice', () => {
    const state = createControlAnswerState({
      selections: {
        1: ['S'],
      },
      customTexts: { 1: 'XL' },
    })
    expect(valueOf(questionDecision(state, { questions }), 'question').answers[1]).toMatchObject({
      selected: ['S'],
      customText: '',
    })
  })

  it('keeps the typed text beside the selection of a single choice where the prompt preserves notes', () => {
    const state = createControlAnswerState({
      selections: {
        1: ['S'],
      },
      customTexts: { 1: 'slim fit' },
    })
    expect(valueOf(questionDecision(state, { questions, preservesSelectionNotes: true }), 'question').answers[1]).toMatchObject({
      selected: ['S'],
      customText: 'slim fit',
    })
  })

  it('sends the typed text of a single choice with no selection', () => {
    const state = createControlAnswerState({
      customTexts: { 1: 'XL' },
    })
    expect(valueOf(questionDecision(state, { questions }), 'question').answers[1]).toMatchObject({ selected: [], customText: 'XL' })
  })

  it('sends the typed text of a question with no option', () => {
    const state = createControlAnswerState({
      customTexts: { 2: 'Ship it on Friday' },
    })
    expect(valueOf(questionDecision(state, { questions }), 'question').answers[2]?.customText).toBe('Ship it on Friday')
  })

  it('copies the selection rather than sharing the state\'s array', () => {
    const selected = ['Red']
    const state = createControlAnswerState({
      selections: { 0: selected },
    })
    const decision = questionDecision(state, { questions })
    selected.push('Blue')
    expect(valueOf(decision, 'question').answers[0]?.selected).toEqual(['Red'])
  })

  it('answers no question for a prompt that asks none', () => {
    const decision = questionDecision(createControlAnswerState({
      selections: {
        0: ['Red'],
      },
    }), { questions: [] })
    expect(valueOf(decision, 'question')).toMatchObject({ answers: [], cancelled: false })
  })
})

describe('typedWithOptions', () => {
  it('keeps the typed text beside the chosen options of a multiple choice', () => {
    expect(typedWithOptions({ question: 'Which?', multiSelect: true, options: [] }, false)).toBe(true)
  })

  it('keeps both on a single choice only where the prompt preserves notes', () => {
    const single = { question: 'Which?', options: [] }
    expect(typedWithOptions(single, false)).toBe(false)
    expect(typedWithOptions({ ...single, multiSelect: false }, true)).toBe(true)
  })

  it('keeps nothing for an absent question unless the prompt preserves notes', () => {
    expect(typedWithOptions(undefined, false)).toBe(false)
    expect(typedWithOptions(undefined, true)).toBe(true)
  })
})

describe('cancelQuestionDecision', () => {
  it('cancels with the reason', () => {
    expect(valueOf(cancelQuestionDecision('not now'), 'question')).toMatchObject({ cancelled: true, feedback: 'not now', answers: [] })
  })

  it('cancels with no reason', () => {
    expect(valueOf(cancelQuestionDecision(), 'question')).toMatchObject({ cancelled: true, feedback: '' })
  })
})

describe('elicitationDecision', () => {
  it('accepts with the form as JSON, keeping zero, false and the empty string', () => {
    const value = valueOf(elicitationDecision(MCP_ELICITATION_ACTION.Accept, { count: 0, enabled: false, text: '' }, 'session'), 'elicitation')
    expect(value.action).toBe(ElicitationAction.ACCEPT)
    expect(JSON.parse(value.contentJson)).toEqual({ count: 0, enabled: false, text: '' })
    expect(value.acceptChoiceKey).toBe('session')
  })

  it('accepts a form with no content as no content', () => {
    expect(valueOf(elicitationDecision(MCP_ELICITATION_ACTION.Accept), 'elicitation')).toMatchObject({
      action: ElicitationAction.ACCEPT,
      contentJson: '',
      acceptChoiceKey: '',
    })
  })

  it.each([
    [MCP_ELICITATION_ACTION.Decline, ElicitationAction.DECLINE],
    [MCP_ELICITATION_ACTION.Cancel, ElicitationAction.CANCEL],
  ])('sends no form and no accept choice with %s', (action, wire) => {
    const value = valueOf(elicitationDecision(action, { secret: 'unused' }, 'session'), 'elicitation')
    expect(value).toMatchObject({ action: wire, contentJson: '', acceptChoiceKey: '' })
    expect(JSON.stringify(value)).not.toContain('secret')
  })
})

describe('dialogDecision', () => {
  it.each([
    ['confirm', DialogAction.CONFIRM],
    ['refuse', DialogAction.REFUSE],
    ['cancel', DialogAction.CANCEL],
  ] as const)('sends %s with no text', (action, wire) => {
    expect(valueOf(dialogDecision(action), 'dialog')).toMatchObject({ action: wire, value: '' })
  })

  it('sends the text of a submit, including an empty one', () => {
    expect(valueOf(dialogDecision('submit', 'v2'), 'dialog')).toMatchObject({ action: DialogAction.SUBMIT, value: 'v2' })
    expect(valueOf(dialogDecision('submit', ''), 'dialog')).toMatchObject({ action: DialogAction.SUBMIT, value: '' })
  })
})

describe('composerDecision', () => {
  function permission(...options: PermissionOption[]): ControlPrompt {
    return {
      kind: 'permission',
      permission: { options },
    }
  }
  const allowOnce = { optionId: 'a1', kind: 'allow_once' }
  const allowAlways = { optionId: 'a2', kind: 'allow_always' }
  const rejectOnce = { optionId: 'r1', kind: 'reject_once' }
  const rejectAlways = { optionId: 'r2', kind: 'reject_always' }

  it.each(['', '  \n'])('approves a permission once for a send of %j', (text) => {
    expect(valueOf(composerDecision(permission(rejectOnce, allowAlways, allowOnce), text), 'permission')).toMatchObject({ optionId: 'a1', feedback: '' })
  })

  it('refuses a permission once with the typed text as the reason', () => {
    expect(valueOf(composerDecision(permission(allowOnce, rejectAlways, rejectOnce), 'not that file'), 'permission')).toMatchObject({ optionId: 'r1', feedback: 'not that file' })
  })

  it('refuses with the remembered refusal when the prompt offers no one-time refusal', () => {
    expect(valueOf(composerDecision(permission(allowOnce, rejectAlways), 'never'), 'permission')).toMatchObject({ optionId: 'r2', feedback: 'never' })
  })

  it('sends nothing when the prompt offers no option of the needed kind', () => {
    expect(composerDecision(permission(allowAlways, rejectOnce), '')).toBeUndefined()
    expect(composerDecision(permission(allowOnce, allowAlways), 'no')).toBeUndefined()
    expect(composerDecision(permission(), '')).toBeUndefined()
  })

  it('refuses a plan with the typed text, and with none', () => {
    const plan: ControlPrompt = { kind: 'plan', text: '1. Do it' }
    expect(valueOf(composerDecision(plan, 'smaller steps'), 'plan')).toMatchObject({ approve: false, feedback: 'smaller steps' })
    expect(valueOf(composerDecision(plan, ''), 'plan')).toMatchObject({ approve: false, feedback: '' })
  })

  it.each<ControlPrompt>([
    {
      kind: 'question',
      questions: [
        { question: 'Which?', options: [] },
      ],
    },
    {
      kind: 'elicitation',
      elicitation: { mode: 'form', message: 'Fill it' },
    },
    {
      kind: 'dialog',
      dialog: { variant: 'confirm', title: 'Sure?' },
    },
  ])('sends nothing for a $kind prompt', (prompt) => {
    expect(composerDecision(prompt, 'text')).toBeUndefined()
  })
})
