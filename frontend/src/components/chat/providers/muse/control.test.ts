import type { ControlQuestion } from '../../model/question'
import { describe, expect, it, vi } from 'vitest'
import { createControlAnswerState } from '../../controls/types'
import { KIND_ALLOW_ALWAYS, KIND_ALLOW_ONCE, KIND_REJECT_ALWAYS, KIND_REJECT_ONCE } from '../../model/controlPrompt'
import { museAnswers, museAskUserQuestion, museControl, museQuestions } from './control'

function approval(choices: unknown) {
  return {
    method: 'approval/requested',
    params: { sessionId: 'session', approvalId: 'approval', currentRequirementId: { approvalId: 'approval', sourceIndex: 0 }, subject: { toolName: 'bash', command: 'printf native' }, availableChoices: choices },
  }
}

function question(fields: Record<string, unknown> = {}) {
  return { id: 'native-question', question: 'Choose the native answer', header: 'Native question', options: [{ label: 'One', description: 'First', preview: { content: 'Native preview' } }], selection: { mode: 'single' }, ...fields }
}

function questionRequest(questions: unknown) {
  return { method: 'userInput/requested', params: { sessionId: 'session', userInputId: 'native-input', questions } }
}

describe('museControl', () => {
  it.each([' ', '\t\n'])('refuses a blank native choice ID %j before display or denial', (choiceId) => {
    const frame = approval([{ choiceId, label: 'Native choice', decision: 'denied', scope: 'once' }])
    const original = structuredClone(frame)
    expect(museControl.extractControl?.({ payload: frame })).toBeNull()
    expect(() => museControl.buildControlResponse?.(frame, '', 'worker')).toThrow('choices are invalid')
    expect(frame).toEqual(original)
  })
  it.each([
    ['approved', 'once', KIND_ALLOW_ONCE, undefined],
    ['approvedForSession', 'session', KIND_ALLOW_ALWAYS, 'session'],
    ['approvedPolicyAmendment', 'localPersistent', KIND_ALLOW_ALWAYS, 'workspace'],
    ['denied', 'once', KIND_REJECT_ONCE, undefined],
    ['deniedPolicyAmendment', 'localPersistent', KIND_REJECT_ALWAYS, 'workspace'],
    ['timedOut', 'once', KIND_REJECT_ONCE, undefined],
    ['abort', 'once', KIND_REJECT_ONCE, undefined],
  ])('keeps the offered %s choice and its %s scope', (decision, scope, kind, expectedScope) => {
    const frame = approval([{ choiceId: 'choice-0', label: 'Native choice', decision, scope }])
    const original = structuredClone(frame)
    const result = museControl.extractControl?.({ payload: frame })
    expect(result?.kind).toBe('permission')
    if (result?.kind !== 'permission')
      throw new Error('The approval requires a permission request.')
    expect(result.permission.title).toBe('bash')
    expect(result.permission.command).toBe('printf native')
    expect(result.permission.options).toEqual([{ optionId: 'choice-0', name: 'Native choice', kind, ...(expectedScope ? { scope: expectedScope } : {}) }])
    expect(frame).toEqual(original)
  })

  it.each([undefined, null, false, 0, {}, [], [null], [{ choiceId: '', label: 'Native choice', decision: 'approved', scope: 'once' }], [{ choiceId: 'choice', label: '', decision: 'approved', scope: 'once' }], [{ choiceId: 'choice', label: 'Native choice', decision: 'futureDecision', scope: 'once' }], [{ choiceId: 'choice', label: 'Native choice', decision: 'approved', scope: 'futureScope' }]].map(choices => ({ choices })))('returns no invented permission choices for malformed data $choices', ({ choices }) => {
    expect(museControl.extractControl?.({ payload: approval(choices) })).toBeNull()
  })

  it('rejects a partial choice list without dropping its malformed entry', () => {
    expect(museControl.extractControl?.({ payload: approval([null, { choiceId: 'choice', label: 'Native choice', decision: 'approved', scope: 'once' }]) })).toBeNull()
  })

  it('sends the exact worker request ID and native choice ID', async () => {
    const sender = vi.fn().mockResolvedValue(undefined)
    await museControl.sendPermissionOption?.(sender, 'worker-request:0', 'native-choice:0')
    expect(sender).toHaveBeenCalledOnce()
    expect(JSON.parse(new TextDecoder().decode(sender.mock.calls[0]?.[0]))).toEqual({ jsonrpc: '2.0', id: 'worker-request:0', result: { choiceId: 'native-choice:0' } })
  })

  it('keeps typed denial feedback on the offered feedback choice', () => {
    const frame = approval([
      { choiceId: 'no-feedback', label: 'Deny', decision: 'denied', scope: 'once' },
      { choiceId: 'with-feedback', label: 'Deny with feedback', decision: 'denied', scope: 'once', acceptsFeedback: true },
    ])
    expect(museControl.buildControlResponse?.(frame, '  Native feedback\n', 'worker-request')).toEqual({ jsonrpc: '2.0', id: 'worker-request', result: { choiceId: 'with-feedback', feedback: '  Native feedback\n' } })
    expect(museControl.buildControlResponse?.(frame, '', 'worker-request')).toEqual({ jsonrpc: '2.0', id: 'worker-request', result: { choiceId: 'no-feedback' } })
  })

  it('refuses feedback when no native denial choice accepts it', () => {
    expect(() => museControl.buildControlResponse?.(approval([{ choiceId: 'deny', label: 'Deny', decision: 'denied', scope: 'once' }]), 'Feedback', 'worker')).toThrow('no denial choice')
  })

  it.each([
    { label: 'absent list', choices: undefined },
    { label: 'null list', choices: null },
    { label: 'empty list', choices: [] },
    { label: 'null entry', choices: [null] },
    { label: 'absent ID', choices: [{ label: 'Deny', decision: 'denied', scope: 'once' }] },
    { label: 'empty ID', choices: [{ choiceId: '', label: 'Deny', decision: 'denied', scope: 'once' }] },
    { label: 'numeric ID', choices: [{ choiceId: 0, label: 'Deny', decision: 'denied', scope: 'once' }] },
    { label: 'null ID', choices: [{ choiceId: null, label: 'Deny', decision: 'denied', scope: 'once' }] },
    { label: 'absent label', choices: [{ choiceId: 'deny', decision: 'denied', scope: 'once' }] },
    { label: 'unknown scope', choices: [{ choiceId: 'deny', label: 'Deny', decision: 'denied', scope: 'futureScope' }] },
    { label: 'invalid feedback flag', choices: [{ choiceId: 'deny', label: 'Deny', decision: 'denied', scope: 'once', acceptsFeedback: 'true' }] },
    { label: 'partial list', choices: [null, { choiceId: 'deny', label: 'Deny', decision: 'denied', scope: 'once' }] },
    { label: 'duplicate IDs', choices: [{ choiceId: 'deny', label: 'Deny', decision: 'denied', scope: 'once' }, { choiceId: 'deny', label: 'Deny again', decision: 'denied', scope: 'once' }] },
  ])('refuses an independent denial reply for $label', ({ choices }) => {
    const frame = approval(choices)
    const original = structuredClone(frame)
    expect(() => museControl.buildControlResponse?.(frame, '', 'worker-request')).toThrow()
    expect(frame).toEqual(original)
  })
})

describe('museQuestions', () => {
  it.each([
    { label: 'explicit zero', selection: { mode: 'multiple', minSelections: 0, maxSelections: 0 }, expected: { minimumSelections: 0, maximumSelections: 0 } },
    { label: 'explicit positive limits', selection: { mode: 'multiple', minSelections: 2, maxSelections: 3 }, expected: { minimumSelections: 2, maximumSelections: 3 } },
    { label: 'only a minimum', selection: { mode: 'multiple', minSelections: 2 }, expected: { minimumSelections: 2 } },
    { label: 'only a maximum', selection: { mode: 'multiple', maxSelections: 2 }, expected: { maximumSelections: 2 } },
  ])('preserves $label from the native multiple-selection form', ({ selection, expected }) => {
    const frame = questionRequest([question({ options: [{ label: 'One' }, { label: 'Two' }, { label: 'Three' }], selection })])
    const original = structuredClone(frame)
    const result = museQuestions(frame)
    expect(result).toHaveLength(1)
    expect(result[0]).toMatchObject(expected)
    expect(frame).toEqual(original)
  })

  it.each(['single', 'multiple'])('keeps absent counts absent in native %s mode', (mode) => {
    const frame = questionRequest([question({ selection: { mode } })])
    const original = structuredClone(frame)
    const result = museQuestions(frame)
    expect(result).toHaveLength(1)
    expect(result[0]).not.toHaveProperty('minimumSelections')
    expect(result[0]).not.toHaveProperty('maximumSelections')
    expect(frame).toEqual(original)
  })

  it('does not project numeric option limits from single mode', () => {
    const frame = questionRequest([question({ selection: { mode: 'single', minSelections: 0, maxSelections: 1 } })])
    const original = structuredClone(frame)
    const result = museQuestions(frame)
    expect(result).toHaveLength(1)
    expect(result[0]).not.toHaveProperty('minimumSelections')
    expect(result[0]).not.toHaveProperty('maximumSelections')
    expect(frame).toEqual(original)
  })
  it.each([
    { label: 'absent header', fields: { header: undefined } },
    { label: 'null header', fields: { header: null } },
    { label: 'numeric header', fields: { header: 0 } },
    { label: 'blank ID', fields: { id: ' \t\n' } },
    { label: 'null selection minimum', fields: { selection: { mode: 'multiple', minSelections: null } } },
    { label: 'null selection maximum', fields: { selection: { mode: 'multiple', maxSelections: null } } },
    { label: 'negative minimum', fields: { selection: { mode: 'multiple', minSelections: -1 } } },
    { label: 'negative maximum', fields: { selection: { mode: 'multiple', maxSelections: -1 } } },
    { label: 'conflicting counts', fields: { selection: { mode: 'multiple', minSelections: 1, maxSelections: 0 } } },
    { label: 'excessive maximum', fields: { selection: { mode: 'multiple', maxSelections: 2 } } },
    { label: 'numeric-string minimum', fields: { selection: { mode: 'multiple', minSelections: '0' } } },
    { label: 'fractional maximum', fields: { selection: { mode: 'multiple', maxSelections: 0.5 } } },
  ])('refuses the complete native form for $label', ({ fields }) => {
    const frame = questionRequest([question(), question({ id: 'later-question', ...fields })])
    const original = structuredClone(frame)
    expect(museQuestions(frame)).toEqual([])
    expect(museAskUserQuestion.isRequest(frame)).toBe(false)
    expect(frame).toEqual(original)
  })

  it('keeps an empty native header and exact nonblank question ID', () => {
    const frame = questionRequest([question({ id: ' native 界 ', header: '', options: [] })])
    const original = structuredClone(frame)
    expect(museQuestions(frame)).toEqual([{ id: ' native 界 ', header: '', question: 'Choose the native answer', options: [], multiSelect: false }])
    expect(frame).toEqual(original)
  })
  it('keeps the native question ID and option preview', () => {
    expect(museQuestions(questionRequest([question()]))).toEqual([{ id: 'native-question', header: 'Native question', question: 'Choose the native answer', options: [{ label: 'One', value: 'One', description: 'First', preview: 'Native preview' }], multiSelect: false }])
  })

  it('keeps an empty option list for a native free-text question', () => {
    expect(museQuestions(questionRequest([question({ options: [] })]))[0]?.options).toEqual([])
  })

  it.each(['single', 'multiple'])('keeps empty free text unavailable in native %s mode', (mode) => {
    const frame = questionRequest([question({ selection: { mode }, options: [] })])
    const original = structuredClone(frame)
    expect(museQuestions(frame)[0]?.allowEmpty).toBeUndefined()
    expect(museQuestions(frame)[0]?.multiSelect).toBe(mode === 'multiple')
    expect(frame).toEqual(original)
  })

  it.each([undefined, null, false, 0, {}, [], [null], [question({ id: '' })], [question({ question: '' })], [question({ options: [null] })], [question({ selection: { mode: 'futureMode' } })], [question(), null], [question(), question()]].map(questions => ({ questions })))('rejects a malformed complete question list $questions', ({ questions }) => {
    expect(museQuestions(questionRequest(questions))).toEqual([])
    expect(museAskUserQuestion.isRequest(questionRequest(questions))).toBe(false)
  })
})

describe('museAnswers', () => {
  const questions: ControlQuestion[] = [
    { id: 'single', question: 'Single', options: [{ label: 'One' }], multiSelect: false },
    { id: 'multiple', question: 'Multiple', options: [{ label: 'First' }, { label: 'Second' }], multiSelect: true },
    { id: 'text', question: 'Text', options: [] },
  ]

  it('keeps exact selections and independent typed notes', () => {
    const state = createControlAnswerState({ selections: { 0: ['One'], 1: ['First', 'Second'] }, customTexts: { 0: '  Single note  ', 1: 'Multiple\nnote', 2: '  Free text\n' } })
    expect(museAnswers(questions, state)).toEqual([
      { questionId: 'single', selectedLabel: 'One', note: '  Single note  ' },
      { questionId: 'multiple', selectedLabels: ['First', 'Second'], note: 'Multiple\nnote' },
      { questionId: 'text', freeText: '  Free text\n' },
    ])
  })

  it('keeps empty text distinct from cancellation', async () => {
    const state = createControlAnswerState()
    const sender = vi.fn().mockResolvedValue(undefined)
    const request = { requestId: 'worker-request', agentId: 'agent', payload: questionRequest([question({ options: [] })]) }
    await museAskUserQuestion.sendAnswer(request, sender, [{ id: 'native-question', question: 'Text', options: [] }], state)
    expect(JSON.parse(new TextDecoder().decode(sender.mock.calls[0]?.[0]))).toEqual({ jsonrpc: '2.0', id: 'worker-request', result: { answers: [{ questionId: 'native-question', freeText: '' }] } })
    await museAskUserQuestion.sendReject(request, sender, 'Cancel')
    expect(JSON.parse(new TextDecoder().decode(sender.mock.calls[1]?.[0]))).toEqual({ jsonrpc: '2.0', id: 'worker-request', result: { cancelled: true, reason: 'Cancel' } })
  })

  it.each([undefined, null, 0, false, {}, []].map(questions => ({ questions })))('keeps native cancellation available for unreadable questions $questions', async ({ questions }) => {
    const sender = vi.fn().mockResolvedValue(undefined)
    const request = { requestId: 'worker-request', agentId: 'agent', payload: questionRequest(questions) }
    const original = structuredClone(request)
    await museAskUserQuestion.sendReject(request, sender, 'Cancel')
    expect(sender).toHaveBeenCalledOnce()
    expect(JSON.parse(new TextDecoder().decode(sender.mock.calls[0]?.[0]))).toEqual({ jsonrpc: '2.0', id: 'worker-request', result: { cancelled: true, reason: 'Cancel' } })
    expect(request).toEqual(original)
  })
})

describe('museControl native once denial', () => {
  it.each([
    { decision: 'denied', scope: 'once', acceptsFeedback: true },
    { decision: 'abort', scope: 'once', acceptsFeedback: true },
  ])('selects the offered $decision once choice and keeps exact feedback', ({ decision, scope, acceptsFeedback }) => {
    const frame = approval([
      { choiceId: 'allow_once', label: 'Allow once', decision: 'approved', scope: 'once' },
      { choiceId: 'native-reject', label: 'Reject', decision, scope, acceptsFeedback },
    ])
    const original = structuredClone(frame)
    expect(museControl.buildControlResponse?.(frame, '  Native feedback 界\n', 'worker-request')).toEqual({ jsonrpc: '2.0', id: 'worker-request', result: { choiceId: 'native-reject', feedback: '  Native feedback 界\n' } })
    expect(museControl.buildControlResponse?.(frame, '', 'worker-request')).toEqual({ jsonrpc: '2.0', id: 'worker-request', result: { choiceId: 'native-reject' } })
    expect(frame).toEqual(original)
  })

  it.each([
    { decision: 'denied', scope: 'session' },
    { decision: 'denied', scope: 'localPersistent' },
    { decision: 'abort', scope: 'session' },
    { decision: 'abort', scope: 'localPersistent' },
    { decision: 'futureDecision', scope: 'once' },
    { decision: 'abort', scope: 'futureScope' },
    { decision: 'timedOut', scope: 'once' },
  ])('refuses an implicit $decision denial with $scope scope', ({ decision, scope }) => {
    const frame = approval([{ choiceId: 'native-choice', label: 'Native choice', decision, scope }])
    const original = structuredClone(frame)
    expect(() => museControl.buildControlResponse?.(frame, '', 'worker-request')).toThrow()
    expect(frame).toEqual(original)
  })

  it('refuses typed feedback for an abort choice that does not accept it', () => {
    const frame = approval([{ choiceId: 'abort', label: 'Reject', decision: 'abort', scope: 'once', acceptsFeedback: false }])
    expect(() => museControl.buildControlResponse?.(frame, 'Native feedback', 'worker-request')).toThrow('no denial choice')
  })
})

describe('museAskUserQuestion native cancellation reason', () => {
  it.each(['', ' \t\n', 'The user declined this question.', '  Native reason 界\n'])('keeps the exact native cancellation reason %j', async (reason) => {
    const sender = vi.fn().mockResolvedValue(undefined)
    const request = { requestId: 'worker-request', agentId: 'agent', payload: questionRequest([question()]) }
    const original = structuredClone(request)
    await museAskUserQuestion.sendReject(request, sender, reason)
    expect(sender).toHaveBeenCalledOnce()
    expect(JSON.parse(new TextDecoder().decode(sender.mock.calls[0]?.[0]))).toEqual({ jsonrpc: '2.0', id: 'worker-request', result: { cancelled: true, reason } })
    expect(request).toEqual(original)
  })
})
