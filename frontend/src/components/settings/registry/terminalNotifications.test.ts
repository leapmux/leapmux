import { beforeEach, describe, expect, it, vi } from 'vitest'
import { requestTerminalOsNotifications } from './terminalNotifications'

// The OS permission prompt is the one dependency the ON path turns on.
const requestPermission = vi.hoisted(() => vi.fn<() => Promise<boolean>>())
vi.mock('~/lib/osNotification', () => ({ requestOsNotificationPermission: requestPermission }))

/**
 * The ON path of the terminal-OS-notifications toggle. The permission flow
 * had no coverage: the toggle shipped as an inline async callback, so the
 * only way to reach it was a full component render against the real
 * PreferencesProvider.
 */
describe('requestTerminalOsNotifications', () => {
  beforeEach(() => {
    requestPermission.mockReset()
  })

  it('stores true when the user grants permission', async () => {
    requestPermission.mockResolvedValue(true)
    await expect(requestTerminalOsNotifications()).resolves.toBe(true)
    expect(requestPermission).toHaveBeenCalledTimes(1)
  })

  it('stays off when the user declines', async () => {
    // The branch that matters: showing the toggle ON with no OS permission
    // behind it would promise notifications that can never arrive.
    requestPermission.mockResolvedValue(false)
    await expect(requestTerminalOsNotifications()).resolves.toBe(false)
    expect(requestPermission).toHaveBeenCalledTimes(1)
  })

  // The OFF branch is NOT here, and must not come back: it lives at the
  // toggle, which stores false without calling this at all. Re-asking for a
  // permission the user is in the act of switching off is prompt fatigue,
  // and a denied origin answers from its own sticky decision, which would
  // flip the toggle back on.
  it('propagates a rejected permission request to its caller', async () => {
    requestPermission.mockRejectedValue(new Error('permission API unavailable'))
    await expect(requestTerminalOsNotifications()).rejects.toThrow('permission API unavailable')
  })
})
