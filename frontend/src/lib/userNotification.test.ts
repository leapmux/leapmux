import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { showUserNotification } from './userNotification'

const showNotificationToast = vi.hoisted(() => vi.fn())
vi.mock('~/components/common/Toast', () => ({ showNotificationToast }))

const showDesktopNotification = vi.hoisted(() => vi.fn())
vi.mock('~/lib/osNotification', () => ({ showDesktopNotification }))

function setVisibility(state: DocumentVisibilityState) {
  Object.defineProperty(document, 'visibilityState', { value: state, configurable: true })
}

describe('showUserNotification', () => {
  beforeEach(() => {
    showNotificationToast.mockClear()
    showDesktopNotification.mockClear()
  })

  afterEach(() => {
    setVisibility('visible')
  })

  it('shows a toast and no desktop notification while the page is visible', () => {
    setVisibility('visible')
    const notification = {
      id: 'notice',
      title: 'Tests passed',
      body: 'All 42',
      sourceTabTitle: 'Builder',
    }
    showUserNotification(notification)

    expect(showNotificationToast).toHaveBeenCalledExactlyOnceWith(notification)
    expect(showDesktopNotification).not.toHaveBeenCalled()
  })

  // The toast stays for when the user comes back; the desktop notification is
  // what reaches them while they look elsewhere.
  it('shows a toast and a desktop notification while the page is hidden', () => {
    setVisibility('hidden')
    showUserNotification({
      id: 'notice',
      title: 'Build failed',
      body: 'error: 3 tests failed',
      sourceTabTitle: 'Builder',
    })

    expect(showNotificationToast).toHaveBeenCalledOnce()
    expect(showDesktopNotification).toHaveBeenCalledOnce()
    const [desktop] = showDesktopNotification.mock.calls[0]!
    expect(desktop.title).toBe('Build failed')
    expect(desktop.body).toBe('error: 3 tests failed\nFrom Builder')
  })

  it.each([
    [
      'no body and no source',
      '',
      undefined,
      '',
    ],
    // Without the filter, an empty body would open the text with a blank line.
    [
      'no body',
      '',
      'Builder',
      'From Builder',
    ],
    [
      'no source',
      'All 42',
      undefined,
      'All 42',
    ],
    [
      'an empty source title',
      'All 42',
      '',
      'All 42',
    ],
  ])('leaves the empty parts out of the desktop notification with %s', (_name, body, sourceTabTitle, desktopBody) => {
    setVisibility('hidden')
    showUserNotification({
      id: 'notice',
      title: 'Done',
      body,
      sourceTabTitle,
    })

    expect(showDesktopNotification.mock.calls[0]![0].body).toBe(desktopBody)
  })

  // The toast already showed the notification, so a desktop notification that
  // the platform refuses must not show a second toast.
  it('shows one toast when the desktop notification is unavailable', () => {
    setVisibility('hidden')
    showDesktopNotification.mockImplementationOnce((_opts: unknown, onUnavailable: () => void) => onUnavailable())
    showUserNotification({
      id: 'notice',
      title: 'Done',
      body: '',
      sourceTabTitle: undefined,
    })

    expect(showDesktopNotification).toHaveBeenCalledOnce()
    expect(showNotificationToast).toHaveBeenCalledOnce()
  })

  it('uses one tag for the same notification even while windows resolve its source differently', () => {
    setVisibility('hidden')
    showUserNotification({
      id: 'notice',
      title: 'Done',
      body: 'All 42',
      sourceTabTitle: 'Builder',
    })
    showUserNotification({
      id: 'notice',
      title: 'Done',
      body: 'All 42',
      sourceTabTitle: undefined,
    })

    const [first, second] = showDesktopNotification.mock.calls.map(call => call[0].tag)
    expect(first).toBe('leapmux-notify:notice')
    expect(second).toBe(first)
  })

  it('keeps distinct posts with the same text separate', () => {
    setVisibility('hidden')
    showUserNotification({
      id: 'first',
      title: 'Done',
      body: '',
      sourceTabTitle: undefined,
    })
    showUserNotification({
      id: 'second',
      title: 'Done',
      body: '',
      sourceTabTitle: undefined,
    })

    const [first, second] = showDesktopNotification.mock.calls.map(call => call[0].tag)
    expect(first).not.toBe(second)
  })
})
