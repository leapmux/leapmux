import { describe, expect, it } from 'vitest'
import { createControlAnswerState, createControlSwitch, toRpcId } from './types'

describe('toRpcId', () => {
  it('converts numeric string to number', () => {
    expect(toRpcId('42')).toBe(42)
  })

  it('preserves non-numeric string', () => {
    expect(toRpcId('abc')).toBe('abc')
  })

  it('converts zero', () => {
    expect(toRpcId('0')).toBe(0)
  })

  it('converts a negative integer string to a number', () => {
    expect(toRpcId('-5')).toBe(-5)
  })

  it('preserves a UUID-style id (non-numeric), the Claude/ACP request-id case', () => {
    expect(toRpcId('abc-123')).toBe('abc-123')
  })
})

describe('createControlAnswerState', () => {
  it('starts every field empty with no seed', () => {
    const state = createControlAnswerState()
    expect(state.selections()).toEqual({})
    expect(state.customTexts()).toEqual({})
    expect(state.currentPage()).toBe(0)
    expect(state.switches()).toEqual({})
  })

  // A partial seed is the shape that comes back from storage: an older record
  // carries no `switches` key at all, and it must not become `undefined`.
  it('fills the absent fields of a partial seed with the empty defaults', () => {
    const state = createControlAnswerState({ selections: { 0: ['Postgres'] } })
    expect(state.selections()).toEqual({ 0: ['Postgres'] })
    expect(state.customTexts()).toEqual({})
    expect(state.currentPage()).toBe(0)
    expect(state.switches()).toEqual({})
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
    const remember = createControlSwitch(() => state, 'control-remember-checkbox')
    const bypass = createControlSwitch(() => state, 'control-bypass-permissions-checkbox')

    remember.set(true)
    bypass.set(true)
    remember.set(false)

    expect(remember.checked()).toBe(false)
    expect(bypass.checked()).toBe(true)
  })

  // The record is captured ONCE, at creation. A caller that builds one inline in
  // JSX makes the prop a getter, and a per-access read would then mint a fresh
  // empty record and silently lose the choice the user just made.
  it('binds the record it was created with, not the one a later read returns', () => {
    const bound = createControlAnswerState()
    let read = bound
    const bypass = createControlSwitch(() => read, 'control-bypass-permissions-checkbox')

    bypass.set(true)
    read = createControlAnswerState()

    expect(bypass.checked()).toBe(true)
    expect(bound.switches()).toEqual({ 'control-bypass-permissions-checkbox': true })
  })
})
