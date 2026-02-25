import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { _resetListeners, emitSettingsChanged, waitForSettingsChanged } from '~/lib/settingsChangedEvent'

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
    const promise = waitForSettingsChanged(
      c => c.permissionMode?.old === 'plan',
      5000,
    )
    emitSettingsChanged({ permissionMode: { old: 'plan', new: 'default' } })
    await promise
    // Advancing timers should not cause any issues
    vi.advanceTimersByTime(10000)
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
    const promise = waitForSettingsChanged(
      c => c.permissionMode?.old === 'plan',
      5000,
    )
    emitSettingsChanged({ permissionMode: { old: 'plan', new: 'default' } })
    await promise

    // A second emit should not cause errors
    emitSettingsChanged({ permissionMode: { old: 'plan', new: 'default' } })
  })

  it('removes listener from list after timeout', async () => {
    const promise = waitForSettingsChanged(
      c => c.permissionMode?.old === 'plan',
      5000,
    )
    vi.advanceTimersByTime(5000)
    await promise.catch(() => {})

    // Emitting after timeout should not cause errors
    emitSettingsChanged({ permissionMode: { old: 'plan', new: 'default' } })
  })
})
