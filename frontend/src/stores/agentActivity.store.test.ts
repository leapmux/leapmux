import { describe, expect, it } from 'vitest'
import { createAgentActivityStore } from '~/stores/agentActivity.store'

describe('createAgentActivityStore', () => {
  it('reports an agent nothing has spoken about as idle', () => {
    const store = createAgentActivityStore()

    // A spinner shown before any evidence is worse than one shown a beat late.
    expect(store.isBusy('never-seen')).toBe(false)
  })

  it('holds what the worker last said', () => {
    const store = createAgentActivityStore()

    store.setBusy('a1', true)
    expect(store.isBusy('a1')).toBe(true)

    store.setBusy('a1', false)
    expect(store.isBusy('a1')).toBe(false)
  })

  it('reports whether a write actually changed the value', () => {
    const store = createAgentActivityStore()

    // The turn-end alert reads this return. The worker broadcasts on transition,
    // but the same value still reaches a client twice -- a catch-up replay
    // landing beside a live event -- and alerting on arrival would ring twice.
    expect(store.setBusy('a1', true)).toBe(true)
    expect(store.setBusy('a1', true)).toBe(false)
    expect(store.setBusy('a1', false)).toBe(true)
    expect(store.setBusy('a1', false)).toBe(false)
  })

  it('treats the first false as a change, not as a no-op against the default', () => {
    const store = createAgentActivityStore()

    // Hydration seeds an idle agent, and that write must land: otherwise a later
    // busy -> idle transition would be the FIRST recorded false and would alert
    // for work the user never saw start.
    expect(store.setBusy('a1', false)).toBe(true)
  })

  it('keeps agents independent', () => {
    const store = createAgentActivityStore()

    store.setBusy('a1', true)
    store.setBusy('a2', false)

    expect(store.isBusy('a1')).toBe(true)
    expect(store.isBusy('a2')).toBe(false)
  })

  it('forgets one agent on tab close', () => {
    const store = createAgentActivityStore()
    store.setBusy('a1', true)
    store.setBusy('a2', true)

    store.forget('a1')

    expect(store.isBusy('a1')).toBe(false)
    expect(store.isBusy('a2')).toBe(true)
  })
})
