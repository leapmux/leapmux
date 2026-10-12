import { describe, expect, it } from 'vitest'
import { museTodoSpec } from './todo'

describe('museTodoSpec', () => {
  it.each([
    ['pending', 'pending'],
    ['in_progress', 'in_progress'],
    ['completed', 'completed'],
    ['cancelled', 'deleted'],
  ])('reads the declared model %s status and exact text', (native, status) => {
    const args = { todos: [{ text: ' \n Native task 文 \n ', status: native }] }
    const before = structuredClone(args)
    const spec = museTodoSpec({ args, hasResult: false, failed: false, output: '' })
    expect(spec).toEqual({
      kind: 'todo',
      request: { items: [{ rowKey: '0: \n Native task 文 \n ', content: ' \n Native task 文 \n ', status, activeForm: '' }] },
    })
    expect(args).toEqual(before)
  })

  it('keeps an actual empty list separate from an absent list', () => {
    expect(museTodoSpec({ args: { todos: [] }, hasResult: false, failed: false, output: '' }))
      .toEqual({ kind: 'todo', request: { items: [] } })
    const absent = museTodoSpec({ args: {}, hasResult: false, failed: false, output: '' })
    expect(absent.kind).toBe('other')
    expect(absent.request).toEqual({ args: {} })
    expect(absent.result).toBeUndefined()
  })

  it.each([
    null,
    0,
    '',
    {},
    [null],
    [{}],
    [{ text: '', status: 'pending' }],
    [{ text: ' \n ', status: 'pending' }],
    [{ text: 0, status: 'pending' }],
    [{ text: 'Native task', status: null }],
    [{ text: 'Native task', status: 'future' }],
    [{ text: 'Native task', status: 'inProgress' }],
    [{ text: 'Valid earlier task', status: 'pending' }, { text: 'Invalid later task', status: 'future' }],
  ])('keeps a malformed model list as raw arguments without a partial projection: %j', (todos) => {
    const args = { todos }
    const spec = museTodoSpec({ args, hasResult: false, failed: false, output: '' })
    expect(spec.kind).toBe('other')
    expect(spec.request).toEqual({ args })
    expect(spec.result).toBeUndefined()
  })

  it('does not copy an event active form into the model request', () => {
    const spec = museTodoSpec({
      args: { todos: [{ text: 'Native task', status: 'in_progress', activeForm: 'An event-only field' }] },
      hasResult: false,
      failed: false,
      output: '',
    })
    expect(spec.request).toEqual({ items: [{ rowKey: '0:Native task', content: 'Native task', status: 'in_progress', activeForm: '' }] })
  })

  it('keeps an actual result and its native preview separate from the request', () => {
    const spec = museTodoSpec({ args: { todos: [{ text: 'Native task', status: 'pending' }] }, hasResult: true, failed: false, output: 'Native list updated' })
    expect(spec.request).toEqual({ items: [{ rowKey: '0:Native task', content: 'Native task', status: 'pending', activeForm: '' }] })
    expect(spec.result).toEqual({ items: [{ rowKey: '0:Native task', content: 'Native task', status: 'pending', activeForm: '' }], note: 'Native list updated' })
  })

  it('keeps a native failure without constructing a successful list result', () => {
    const spec = museTodoSpec({ args: { todos: [] }, hasResult: true, failed: true, output: 'The native list update failed' })
    expect(spec.result).toEqual({ failure: true, text: 'The native list update failed' })
  })
})
