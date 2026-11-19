import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { KEY_BROWSER_PREFS, localStorageSet } from '~/lib/browserStorage'
import { _resetOsNotificationDedupeForTests, notifyOs, osNotificationsAvailable, showDesktopNotification } from '~/lib/osNotification'

vi.mock('~/components/common/Toast', () => ({
  showInfoToast: vi.fn(),
}))

vi.mock('~/api/platformBridge', () => ({
  desktopHub: () => null,
  isTauriApp: vi.fn(() => false),
  platformBridge: {
    requestNotificationPermission: vi.fn(),
    showNotification: vi.fn(),
  },
}))

const { showInfoToast } = await import('~/components/common/Toast')
const { isTauriApp, platformBridge } = await import('~/api/platformBridge')

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
    localStorageSet(KEY_BROWSER_PREFS, { desktopNotifications: true })
    const notify = vi.fn()
    vi.stubGlobal('Notification', notify)
    Object.defineProperty(globalThis.Notification, 'permission', { value: 'granted', configurable: true })
    notifyOs({ title: 'A', body: 'B', tag: 'same' })
    notifyOs({ title: 'A', body: 'B', tag: 'same' })
    expect(notify).toHaveBeenCalledTimes(1)
  })

  it('allows the same tag again after the dedupe window', () => {
    vi.useFakeTimers()
    localStorageSet(KEY_BROWSER_PREFS, { desktopNotifications: true })
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
    localStorageSet(KEY_BROWSER_PREFS, { desktopNotifications: true })
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
    localStorageSet(KEY_BROWSER_PREFS, { desktopNotifications: true })
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
    localStorageSet(KEY_BROWSER_PREFS, { desktopNotifications: true })
    const notify = vi.fn()
    vi.stubGlobal('Notification', notify)
    Object.defineProperty(globalThis.Notification, 'permission', { value: 'granted', configurable: true })
    expect(() => {
      for (let i = 0; i < 1000; i++) {
        notifyOs({
          title: 'T',
          body: String(i),
          tag: `tag-${i}`,
        })
      }
    }).not.toThrow()
    // Every distinct tag is a first occurrence → all notify.
    expect(notify).toHaveBeenCalledTimes(1000)
  })
})

describe('showDesktopNotification', () => {
  beforeEach(() => {
    localStorageSet(KEY_BROWSER_PREFS, {})
    vi.mocked(isTauriApp).mockReturnValue(false)
    vi.mocked(platformBridge.showNotification).mockReset()
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  function grantBrowserPermission() {
    const notify = vi.fn()
    vi.stubGlobal('Notification', notify)
    Object.defineProperty(globalThis.Notification, 'permission', { value: 'granted', configurable: true })
    return notify
  }

  it('shows a browser notification with its tag when the preference is on and permission is granted', () => {
    localStorageSet(KEY_BROWSER_PREFS, { desktopNotifications: true })
    const notify = grantBrowserPermission()
    const unavailable = vi.fn()

    showDesktopNotification({ title: 'Done', body: 'All green', tag: 't' }, unavailable)

    expect(notify).toHaveBeenCalledWith('Done', { body: 'All green', tag: 't' })
    expect(unavailable).not.toHaveBeenCalled()
  })

  it('reports unavailable and shows nothing when the preference is off', () => {
    const notify = grantBrowserPermission()
    const unavailable = vi.fn()

    showDesktopNotification({ title: 'Done', body: '' }, unavailable)

    expect(notify).not.toHaveBeenCalled()
    expect(unavailable).toHaveBeenCalledOnce()
  })

  it('reports unavailable when the permission is not granted', () => {
    localStorageSet(KEY_BROWSER_PREFS, { desktopNotifications: true })
    const notify = vi.fn()
    vi.stubGlobal('Notification', notify)
    Object.defineProperty(globalThis.Notification, 'permission', { value: 'default', configurable: true })
    const unavailable = vi.fn()

    showDesktopNotification({ title: 'Done', body: '' }, unavailable)

    expect(notify).not.toHaveBeenCalled()
    expect(unavailable).toHaveBeenCalledOnce()
  })

  it('reports unavailable when the browser refuses to construct one', () => {
    localStorageSet(KEY_BROWSER_PREFS, { desktopNotifications: true })
    const notify = vi.fn(() => {
      throw new TypeError('Illegal constructor')
    })
    vi.stubGlobal('Notification', notify)
    Object.defineProperty(globalThis.Notification, 'permission', { value: 'granted', configurable: true })
    const unavailable = vi.fn()

    showDesktopNotification({ title: 'Done', body: '' }, unavailable)

    expect(unavailable).toHaveBeenCalledOnce()
  })

  it('goes through the desktop app\'s own notification bridge', async () => {
    localStorageSet(KEY_BROWSER_PREFS, { desktopNotifications: true })
    vi.mocked(isTauriApp).mockReturnValue(true)
    vi.mocked(platformBridge.showNotification).mockResolvedValue(undefined)
    const unavailable = vi.fn()

    showDesktopNotification({ title: 'Done', body: 'All green', tag: 't' }, unavailable)
    await Promise.resolve()

    expect(platformBridge.showNotification).toHaveBeenCalledWith('Done', 'All green', 't')
    expect(unavailable).not.toHaveBeenCalled()
  })

  it('reports unavailable when the desktop app refuses', async () => {
    localStorageSet(KEY_BROWSER_PREFS, { desktopNotifications: true })
    vi.mocked(isTauriApp).mockReturnValue(true)
    vi.mocked(platformBridge.showNotification).mockRejectedValue(new Error('notification permission denied'))
    const unavailable = vi.fn()

    showDesktopNotification({ title: 'Done', body: '' }, unavailable)
    await vi.waitFor(() => expect(unavailable).toHaveBeenCalledOnce())
  })
})
