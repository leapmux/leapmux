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

  it('reports the busy -> idle edge, and only that edge', () => {
    const store = createAgentActivityStore()

    // The turn-end alert reads this return, so only the settle may answer true.
    expect(store.setBusy('a1', true)).toBe(false)
    expect(store.setBusy('a1', true)).toBe(false)
    expect(store.setBusy('a1', false)).toBe(true)
    // The worker broadcasts on transition, but the same value still reaches a
    // client twice -- a catch-up replay landing beside a live event -- and
    // ringing on arrival would ring twice for one settle.
    expect(store.setBusy('a1', false)).toBe(false)
  })

  it('does not call an idle report for an agent it never saw working a settle', () => {
    const store = createAgentActivityStore()

    // A NOTIFY-mode tab can subscribe after the turn started and receive the
    // idle report alone. Nothing the user watched finished, so nothing rings.
    expect(store.setBusy('a1', false)).toBe(false)
    // The write still lands, so the next real turn settles normally.
    expect(store.setBusy('a1', true)).toBe(false)
    expect(store.setBusy('a1', false)).toBe(true)
  })

  it('does not settle an agent whose state was forgotten mid-turn', () => {
    const store = createAgentActivityStore()
    store.setBusy('a1', true)

    // The worker-offline sweep forgets the agent. When the worker returns, its
    // catch-up replay reports idle -- for a turn that died with the old link,
    // not one this client watched finish.
    store.forget('a1')

    expect(store.setBusy('a1', false)).toBe(false)
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
