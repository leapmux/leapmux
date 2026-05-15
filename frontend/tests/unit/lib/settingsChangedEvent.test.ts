import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { _getListenerCount, _resetListeners, emitSettingsChanged, waitForSettingsChanged } from '~/lib/settingsChangedEvent'

describe('settingsChangedEvent', () => {
  beforeEach(() => {
    _resetListeners()
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('resolves when a matching event is emitted', async () => {
    const promise = waitForSettingsChanged(
      c => c.permissionMode?.old === 'plan',
      5000,
    )
    emitSettingsChanged({ permissionMode: { old: 'plan', new: 'default' } })
    await expect(promise).resolves.toBeUndefined()
  })

  it('does not resolve for non-matching events', async () => {
    let resolved = false
    const promise = waitForSettingsChanged(
      c => c.permissionMode?.old === 'plan',
      5000,
    ).then(() => { resolved = true })

    emitSettingsChanged({ permissionMode: { old: 'default', new: 'bypassPermissions' } })

    await Promise.resolve()
    expect(resolved).toBe(false)

    // Clean up by timing out
    vi.advanceTimersByTime(5000)
    await promise.catch(() => {})
  })

  it('rejects on timeout', async () => {
    const promise = waitForSettingsChanged(
      c => c.permissionMode?.old === 'plan',
      5000,
    )
    vi.advanceTimersByTime(5000)
    await expect(promise).rejects.toThrow('Timed out waiting for settings_changed')
  })

  it('clears timer when resolved before timeout', async () => {
    const rejectSpy = vi.fn()
    const promise = waitForSettingsChanged(
      c => c.permissionMode?.old === 'plan',
      5000,
    ).catch(rejectSpy)
    emitSettingsChanged({ permissionMode: { old: 'plan', new: 'default' } })
    await promise

    // If the resolve path didn't clear the timer, advancing past the deadline
    // would trip it and reject the promise (which we'd catch via rejectSpy).
    vi.advanceTimersByTime(10000)
    await Promise.resolve()
    expect(rejectSpy).not.toHaveBeenCalled()
  })

  it('supports multiple concurrent waiters with different predicates', async () => {
    const p1 = waitForSettingsChanged(
      c => c.permissionMode?.old === 'plan',
      5000,
    )
    const p2 = waitForSettingsChanged(
      c => c.permissionMode?.old === 'default',
      5000,
    )

    emitSettingsChanged({ permissionMode: { old: 'plan', new: 'default' } })
    await expect(p1).resolves.toBeUndefined()

    // p2 should still be pending — resolve it with a matching event
    emitSettingsChanged({ permissionMode: { old: 'default', new: 'bypassPermissions' } })
    await expect(p2).resolves.toBeUndefined()
  })

  it('removes listener from list after match', async () => {
    expect(_getListenerCount()).toBe(0)
    const promise = waitForSettingsChanged(
      c => c.permissionMode?.old === 'plan',
      5000,
    )
    expect(_getListenerCount()).toBe(1)
    emitSettingsChanged({ permissionMode: { old: 'plan', new: 'default' } })
    await promise
    expect(_getListenerCount()).toBe(0)
  })

  it('removes listener from list after timeout', async () => {
    expect(_getListenerCount()).toBe(0)
    const promise = waitForSettingsChanged(
      c => c.permissionMode?.old === 'plan',
      5000,
    )
    expect(_getListenerCount()).toBe(1)
    vi.advanceTimersByTime(5000)
    await promise.catch(() => {})
    expect(_getListenerCount()).toBe(0)
  })
})
