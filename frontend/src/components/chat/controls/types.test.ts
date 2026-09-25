import type { ControlResponseSender } from './types'
import { describe, expect, it, vi } from 'vitest'
import { buildJsonRpcResult, CONTROL_ALLOW_CHOICE_ID, createControlAnswerState, createControlChoice, createControlSwitch, questionsFromWire, sendSelectedOptionResponse } from './types'

/**
 * A cast is not a check.
 *
 * Two plugins tested the OUTER array with `Array.isArray` and asserted the ELEMENTS,
 * so a `null` element reached `q().question` and threw the whole banner away, and a
 * bare string reached it as `undefined` -- a blank dialog the reader can see and
 * cannot answer. `AskUserQuestionControl` then hands `q().options` to a `<For>`,
 * which needs a real array on every element.
 */
describe('questionsFromWire', () => {
  it('answers no questions for anything that is not an array', () => {
    expect(questionsFromWire(undefined)).toEqual([])
    expect(questionsFromWire(null)).toEqual([])
    expect(questionsFromWire({ questions: [] })).toEqual([])
    expect(questionsFromWire('Which one?')).toEqual([])
  })

  it('drops an element that is not an object', () => {
    expect(questionsFromWire([null, 'Which one?', 7, true, undefined])).toEqual([])
  })

  it('keeps the readable questions beside the elements it drops', () => {
    expect(questionsFromWire([null, { question: 'Which one?', options: [{ label: 'A' }] }])).toEqual([
      { question: 'Which one?', options: [{ label: 'A' }] },
    ])
  })

  it('gives an element with no readable text an empty question rather than undefined', () => {
    expect(questionsFromWire([{ options: [{ label: 'A' }] }])).toEqual([{ question: '', options: [{ label: 'A' }] }])
  })

  it('coerces a non-array options field to the empty list the For needs', () => {
    expect(questionsFromWire([{ question: 'Which one?', options: 'A' }])).toEqual([{ question: 'Which one?', options: [] }])
    expect(questionsFromWire([{ question: 'Which one?' }])).toEqual([{ question: 'Which one?', options: [] }])
  })

  // A question that states no option is a real one -- it asks for free text, and
  // `allowEmpty` is what says the provider accepts an empty answer.
  it('keeps a question that offers no option', () => {
    expect(questionsFromWire([{ question: 'Say more', options: [], allowEmpty: true }]))
      .toEqual([{ question: 'Say more', options: [], allowEmpty: true }])
  })

  it('carries every other field the element stated', () => {
    expect(questionsFromWire([{ id: 'q1', question: 'Which one?', header: 'Pick', options: [{ label: 'A' }], multiSelect: true }]))
      .toEqual([{ id: 'q1', question: 'Which one?', header: 'Pick', options: [{ label: 'A' }], multiSelect: true }])
  })
})

describe('control response identity', () => {
  it.each(['42', '0', '-5', '001', '1e3', '9007199254740993', 'abc-123'])('preserves the worker request ID %s', (requestId) => {
    expect(buildJsonRpcResult(requestId, { count: 0, enabled: false, text: '' })).toEqual({
      jsonrpc: '2.0',
      id: requestId,
      result: { count: 0, enabled: false, text: '' },
    })
  })
})

describe('createControlAnswerState', () => {
  it('starts each saved field empty and marks the state ready', () => {
    const state = createControlAnswerState()
    expect(state.selections()).toEqual({})
    expect(state.customTexts()).toEqual({})
    expect(state.currentPage()).toBe(0)
    expect(state.switches()).toEqual({})
    expect(state.choices()).toEqual({})
    expect(state.ready()).toBe(true)
  })

  // A partial seed is the shape that comes back from storage: an older record
  // carries no `switches` key at all, and it must not become `undefined`.
  it('fills the absent fields of a partial seed with the empty defaults', () => {
    const state = createControlAnswerState({ selections: { 0: ['Postgres'] } })
    expect(state.selections()).toEqual({ 0: ['Postgres'] })
    expect(state.customTexts()).toEqual({})
    expect(state.currentPage()).toBe(0)
    expect(state.switches()).toEqual({})
    expect(state.choices()).toEqual({})
  })

  it('seeds the pill choices like every other field', () => {
    const state = createControlAnswerState({ choices: { 'control-permissions-pill': 'bypass' } })
    expect(state.choices()).toEqual({ 'control-permissions-pill': 'bypass' })
  })
})

describe('createControlSwitch', () => {
  it('reports an unset switch as unchecked', () => {
    const state = createControlAnswerState()
    expect(createControlSwitch(() => state, 'plan-clear-context-checkbox').checked()).toBe(false)
  })

  it('writes the switch through to the shared record', () => {
    const state = createControlAnswerState()
    const clear = createControlSwitch(() => state, 'plan-clear-context-checkbox')

    clear.set(true)

    expect(clear.checked()).toBe(true)
    expect(state.switches()).toEqual({ 'plan-clear-context-checkbox': true })
  })

  // Every switch of every control shares ONE map, so the ids are what keep them
  // apart. A write must leave its siblings alone rather than replace the map.
  it('keeps two switches of one record apart by id', () => {
    const state = createControlAnswerState()
    const preview = createControlSwitch(() => state, 'control-preview-checkbox')
    const clear = createControlSwitch(() => state, 'plan-clear-context-checkbox')

    preview.set(true)
    clear.set(true)
    preview.set(false)

    expect(preview.checked()).toBe(false)
    expect(clear.checked()).toBe(true)
  })

  // The record is captured ONCE, at creation. A caller that builds one inline in
  // JSX makes the prop a getter, and a per-access read would then mint a fresh
  // empty record and silently lose the choice the user just made.
  it('binds the record it was created with, not the one a later read returns', () => {
    const bound = createControlAnswerState()
    let read = bound
    const clear = createControlSwitch(() => read, 'plan-clear-context-checkbox')

    clear.set(true)
    read = createControlAnswerState()

    expect(clear.checked()).toBe(true)
    expect(bound.switches()).toEqual({ 'plan-clear-context-checkbox': true })
  })
})

describe('createControlChoice', () => {
  it('reports an unset choice as the fallback', () => {
    const state = createControlAnswerState()
    expect(createControlChoice(() => state, 'control-permissions-pill', 'default').choice()).toBe('default')
  })

  it('reads undefined before any selection when no fallback is passed', () => {
    const state = createControlAnswerState()
    const scope = createControlChoice(() => state, CONTROL_ALLOW_CHOICE_ID)
    expect(scope.choice()).toBeUndefined()

    scope.setChoice('always')
    expect(scope.choice()).toBe('always')
  })

  it('writes the choice through to the shared record', () => {
    const state = createControlAnswerState()
    const pill = createControlChoice(() => state, 'control-permissions-pill', 'default')

    pill.setChoice('bypass')

    expect(pill.choice()).toBe('bypass')
    expect(state.choices()).toEqual({ 'control-permissions-pill': 'bypass' })
  })

  // The permission pill's choice and a switch share one record but not one map,
  // so a choice write must leave the switches untouched and vice versa.
  it('keeps the choices map apart from the switches map', () => {
    const state = createControlAnswerState()
    const pill = createControlChoice(() => state, 'control-permissions-pill', 'default')
    const clear = createControlSwitch(() => state, 'plan-clear-context-checkbox')

    pill.setChoice('bypass')
    clear.set(true)

    expect(state.choices()).toEqual({ 'control-permissions-pill': 'bypass' })
    expect(state.switches()).toEqual({ 'plan-clear-context-checkbox': true })
  })

  // The record is captured ONCE, at creation, for the same reason as a switch:
  // an inline `createControlAnswerState()` prop is a getter, and a per-access
  // read would mint a fresh empty record and lose the user's selection.
  it('binds the record it was created with, not the one a later read returns', () => {
    const bound = createControlAnswerState()
    let read = bound
    const pill = createControlChoice(() => read, 'control-permissions-pill', 'default')

    pill.setChoice('smart')
    read = createControlAnswerState()

    expect(pill.choice()).toBe('smart')
    expect(bound.choices()).toEqual({ 'control-permissions-pill': 'smart' })
  })
})

// The Agent Client Protocol reply that selects one option. The ACP family and MiMo
// Code send each permission answer through it, and the agent reads the literal
// `selected` outcome, so the test pins the wire word rather than the constant.
describe('sendSelectedOptionResponse', () => {
  it('sends the selected option as a JSON-RPC result under the worker request id', async () => {
    const onRespond = vi.fn<ControlResponseSender>().mockResolvedValue(undefined)
    await sendSelectedOptionResponse(onRespond, 'jsonrpc:3', 'reject-1')
    expect(onRespond).toHaveBeenCalledOnce()
    const [bytes, options] = onRespond.mock.calls[0]!
    expect(JSON.parse(new TextDecoder().decode(bytes))).toEqual({ jsonrpc: '2.0', id: 'jsonrpc:3', result: { outcome: { outcome: 'selected', optionId: 'reject-1' } } })
    expect(options).toBeUndefined()
  })

  it('passes a send failure to its caller', async () => {
    const failure = new Error('worker unreachable')
    const onRespond = vi.fn<ControlResponseSender>().mockRejectedValue(failure)
    await expect(sendSelectedOptionResponse(onRespond, 'jsonrpc:3', 'allow-1')).rejects.toBe(failure)
  })
})
