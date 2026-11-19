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
    const notification = { title: 'Tests passed', body: 'All 42', sourceTabTitle: 'Builder' }
    showUserNotification(notification)

    expect(showNotificationToast).toHaveBeenCalledExactlyOnceWith(notification)
    expect(showDesktopNotification).not.toHaveBeenCalled()
  })

  // The toast stays for when the user comes back; the desktop notification is
  // what reaches them while they look elsewhere.
  it('shows a toast and a desktop notification while the page is hidden', () => {
    setVisibility('hidden')
    showUserNotification({ title: 'Build failed', body: 'error: 3 tests failed', sourceTabTitle: 'Builder' })

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
    showUserNotification({ title: 'Done', body, sourceTabTitle })

    expect(showDesktopNotification.mock.calls[0]![0].body).toBe(desktopBody)
  })

  // The toast already showed the notification, so a desktop notification that
  // the platform refuses must not show a second toast.
  it('shows one toast when the desktop notification is unavailable', () => {
    setVisibility('hidden')
    showDesktopNotification.mockImplementationOnce((_opts: unknown, onUnavailable: () => void) => onUnavailable())
    showUserNotification({ title: 'Done', body: '', sourceTabTitle: undefined })

    expect(showDesktopNotification).toHaveBeenCalledOnce()
    expect(showNotificationToast).toHaveBeenCalledOnce()
  })

  // Every open window shows the notification from its own stream. The tag is
  // what makes the browser show one desktop notification for all of them.
  it('tags the desktop notification by its text', () => {
    setVisibility('hidden')
    showUserNotification({ title: 'Done', body: 'x', sourceTabTitle: undefined })
    showUserNotification({ title: 'Done', body: 'x', sourceTabTitle: undefined })
    showUserNotification({ title: 'Done', body: 'y', sourceTabTitle: undefined })

    const tags = showDesktopNotification.mock.calls.map(call => call[0].tag)
    expect(tags[0]).toBe(tags[1])
    expect(tags[2]).not.toBe(tags[0])
  })

  // The same words from two tabs are two notifications: the source is part of
  // the text, so it is part of the tag.
  it('tags notifications from two tabs apart', () => {
    setVisibility('hidden')
    showUserNotification({ title: 'Done', body: 'x', sourceTabTitle: 'Builder' })
    showUserNotification({ title: 'Done', body: 'x', sourceTabTitle: 'Linter' })

    const [first, second] = showDesktopNotification.mock.calls.map(call => call[0].tag)
    expect(first).not.toBe(second)
  })

  // A title can hold a line break, so a tag that joins the title and the body
  // with one would give two different notifications one tag, and the browser
  // would replace the first with the second.
  it('tags a title and a body apart from the same text in the title alone', () => {
    setVisibility('hidden')
    showUserNotification({ title: 'Done', body: 'x\ny', sourceTabTitle: undefined })
    showUserNotification({ title: 'Done\nx', body: 'y', sourceTabTitle: undefined })

    const [first, second] = showDesktopNotification.mock.calls.map(call => call[0].tag)
    expect(first).not.toBe(second)
  })

  it('gives one notification the same tag in every window', () => {
    setVisibility('hidden')
    showUserNotification({ title: 'Done', body: 'x', sourceTabTitle: 'Builder' })
    showUserNotification({ title: 'Done', body: 'x', sourceTabTitle: 'Builder' })

    const [first, second] = showDesktopNotification.mock.calls.map(call => call[0].tag)
    expect(first).toBe(second)
  })
})
