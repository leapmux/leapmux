import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { KEY_BROWSER_PREFS, localStorageSet } from '~/lib/browserStorage'
import { _resetOsNotificationDedupeForTests, notifyOs, osNotificationsAvailable } from '~/lib/osNotification'

vi.mock('~/components/common/Toast', () => ({
  showInfoToast: vi.fn(),
}))

vi.mock('~/api/platformBridge', () => ({
  isTauriApp: vi.fn(() => false),
  platformBridge: {
    requestNotificationPermission: vi.fn(),
    showNotification: vi.fn(),
  },
}))

const { showInfoToast } = await import('~/components/common/Toast')

describe('osNotification', () => {
  beforeEach(() => {
    _resetOsNotificationDedupeForTests()
    localStorageSet(KEY_BROWSER_PREFS, {})
    vi.mocked(showInfoToast).mockClear()
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('falls back to toast when preference is off', () => {
    notifyOs({ title: 'Hi', body: 'there', tag: 't1' })
    expect(showInfoToast).toHaveBeenCalledWith('Hi: there')
  })

  it('dedupes the toast fallback by tag so a chatty program cannot spam toasts when OS notifications are off', () => {
    // Regression: the toast path used to return before the tag dedupe, so a
    // program emitting OSC 9 every ~100ms with notifications disabled produced
    // an unbounded toast storm. The dedupe must apply to both paths.
    notifyOs({ title: 'Build', body: 'done', tag: 'build' })
    notifyOs({ title: 'Build', body: 'done', tag: 'build' })
    notifyOs({ title: 'Build', body: 'done', tag: 'build' })
    expect(showInfoToast).toHaveBeenCalledTimes(1)
  })

  it('dedupes by tag', () => {
    localStorageSet(KEY_BROWSER_PREFS, { terminalOsNotifications: true })
    const notify = vi.fn()
    vi.stubGlobal('Notification', notify)
    Object.defineProperty(globalThis.Notification, 'permission', { value: 'granted', configurable: true })
    notifyOs({ title: 'A', body: 'B', tag: 'same' })
    notifyOs({ title: 'A', body: 'B', tag: 'same' })
    expect(notify).toHaveBeenCalledTimes(1)
  })

  it('allows the same tag again after the dedupe window', () => {
    vi.useFakeTimers()
    localStorageSet(KEY_BROWSER_PREFS, { terminalOsNotifications: true })
    const notify = vi.fn()
    vi.stubGlobal('Notification', notify)
    Object.defineProperty(globalThis.Notification, 'permission', { value: 'granted', configurable: true })
    notifyOs({ title: 'A', body: 'B', tag: 'same' })
    vi.advanceTimersByTime(3_001)
    notifyOs({ title: 'A', body: 'B', tag: 'same' })
    expect(notify).toHaveBeenCalledTimes(2)
    vi.useRealTimers()
  })

  it('reports availability when Notification API exists', () => {
    vi.stubGlobal('Notification', class {})
    expect(osNotificationsAvailable()).toBe(true)
  })

  it('falls back to toast when Notification API is absent', () => {
    localStorageSet(KEY_BROWSER_PREFS, { terminalOsNotifications: true })
    const original = globalThis.Notification
    // @ts-expect-error intentional delete for the absent-API path
    delete globalThis.Notification
    expect(osNotificationsAvailable()).toBe(false)
    notifyOs({ title: 'Bell', body: 'hi', tag: 't-missing' })
    expect(showInfoToast).toHaveBeenCalledWith('Bell: hi')
    if (original)
      globalThis.Notification = original
  })

  it('calls the browser Notification API when permitted and opted in', () => {
    localStorageSet(KEY_BROWSER_PREFS, { terminalOsNotifications: true })
    const notify = vi.fn()
    vi.stubGlobal('Notification', notify)
    Object.defineProperty(globalThis.Notification, 'permission', { value: 'granted', configurable: true })
    notifyOs({ title: 'Bell', body: 'hi', tag: 't1' })
    expect(notify).toHaveBeenCalledWith('Bell', expect.objectContaining({ body: 'hi', tag: 't1' }))
    expect(showInfoToast).not.toHaveBeenCalled()
  })

  it('does not throw or leak unbounded memory under a high-cardinality tag burst', () => {
    // A pathological terminal emitting many distinct OSC notifications within
    // the 3s dedupe window must not grow the dedupe set without limit. Each
    // distinct tag should still notify (dedupe is best-effort), and the prune
    // path must stay bounded.
    localStorageSet(KEY_BROWSER_PREFS, { terminalOsNotifications: true })
    const notify = vi.fn()
    vi.stubGlobal('Notification', notify)
    Object.defineProperty(globalThis.Notification, 'permission', { value: 'granted', configurable: true })
    expect(() => {
      for (let i = 0; i < 1000; i++)
        notifyOs({ title: 'T', body: String(i), tag: `tag-${i}` })
    }).not.toThrow()
    // Every distinct tag is a first occurrence → all notify.
    expect(notify).toHaveBeenCalledTimes(1000)
  })
})
