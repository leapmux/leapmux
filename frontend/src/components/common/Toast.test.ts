import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ChannelError, channelNotOpenError } from '~/lib/channelError'
import { showStickyWarnToast, showWarnToast, showWarnToastUnlessDisconnected, showWarnToastWithLoggedCause } from './Toast'

// The toast host is a runtime global the design system installs; jsdom has no
// such thing, so stand in for it and record what each helper asked for.
const shown: Array<{ el: HTMLElement, duration: number }> = []

beforeEach(() => {
  shown.length = 0
  vi.stubGlobal('ot', {
    toast: {
      // Mirrors @knadh/oat's toastEl: it mounts `el.cloneNode(true)` and
      // returns THAT node, not the one it was handed. Mounting the original
      // instead would let a close button that is inert in a real browser --
      // because cloneNode drops property-assigned handlers -- pass here.
      el: (el: HTMLElement, opts: { duration: number }) => {
        const mounted = el.cloneNode(true) as HTMLElement
        document.body.appendChild(mounted)
        shown.push({ el: mounted, duration: opts.duration })
        return mounted
      },
    },
  })
})

afterEach(() => {
  vi.unstubAllGlobals()
  document.body.replaceChildren()
})

describe('showWarnToast', () => {
  it('dismisses itself, because something is still retrying behind it', () => {
    showWarnToast('a transient problem')

    expect(shown).toHaveLength(1)
    expect(shown[0]!.duration).toBe(3000)
    expect(shown[0]!.el.textContent).toContain('a transient problem')
  })
})

describe('showWarnToastWithLoggedCause', () => {
  // The whole reason it exists: showWarnToast renders err.message, and a
  // transport failure's message names our own plumbing.
  it('renders the caller\'s sentence, not the error\'s', () => {
    showWarnToastWithLoggedCause('Connection to worker lost, reconnecting\u2026', new ChannelError('transport', 'channel disconnected'))

    expect(shown).toHaveLength(1)
    expect(shown[0]!.el.textContent).toContain('Connection to worker lost')
    expect(shown[0]!.el.textContent).not.toContain('channel disconnected')
  })

  it('dismisses itself, because a redial is still running behind it', () => {
    showWarnToastWithLoggedCause('Connection to worker lost, reconnecting\u2026', undefined)

    expect(shown[0]!.duration).toBe(3000)
  })
})

describe('showWarnToastUnlessDisconnected', () => {
  // One dropped socket fails every background load at once. Each one that spoke
  // added another toast, and each read as our own jargon.
  it('says nothing when a dropped link is what failed the operation', () => {
    showWarnToastUnlessDisconnected('Failed to load chat history', channelNotOpenError())
    showWarnToastUnlessDisconnected('Failed to load chat history', new ChannelError('transport', 'channel disconnected'))

    expect(shown).toHaveLength(0)
  })

  // The other half. A worker that answered "no" is not a disconnect, and
  // suppressing that would hide a failure nothing is retrying.
  it('still announces a failure the worker itself reported', () => {
    showWarnToastUnlessDisconnected('Failed to load chat history', new ChannelError('rpc', 'agent not found', { code: 5 }))

    expect(shown).toHaveLength(1)
    expect(shown[0]!.el.textContent).toContain('agent not found')
  })
})

describe('showStickyWarnToast', () => {
  // The one that matters: a terminal close schedules no retry, so a message
  // that expired after three seconds would leave the user with a frozen UI and
  // no explanation of why.
  it('stays until dismissed', () => {
    showStickyWarnToast('this will not fix itself')

    expect(shown).toHaveLength(1)
    expect(shown[0]!.duration).toBe(0)
  })

  // Both of a tab's sockets can be refused by the one hub code path, so the same
  // message can be raised twice for one cause. A second identical sticky toast
  // cannot be dismissed by the same click and reads as two separate faults.
  it('does not stack a second sticky toast while the first is still up', () => {
    showStickyWarnToast('the hub refused this connection')
    showStickyWarnToast('the hub refused this connection')

    expect(shown).toHaveLength(1)
  })

  // The other half, and the reason this dedupe cannot live in a caller: a latch
  // outside the toast never learns the user dismissed it, so it would turn one
  // click into a permanent mute and announce the next real refusal nowhere.
  it('raises the message again once the user has dismissed it', async () => {
    showStickyWarnToast('the hub refused us again')
    shown[0]!.el.querySelector('[data-close]')!.dispatchEvent(new MouseEvent('click'))
    await new Promise(resolve => setTimeout(resolve, 0))

    showStickyWarnToast('the hub refused us again')

    expect(shown).toHaveLength(2)
  })

  // Suppression is per MESSAGE, not "one sticky toast at a time".
  it('still shows a sticky toast whose message differs', () => {
    showStickyWarnToast('your session expired')
    showStickyWarnToast('open in too many places')

    expect(shown).toHaveLength(2)
  })

  // With no auto-dismiss timer, this button is the only way out. It has to be
  // wired onto the node the host actually mounted -- binding it before handing
  // the element over leaves it behind on a copy nobody can click.
  it('dismisses the mounted toast when its close button is clicked', async () => {
    showStickyWarnToast('this will not fix itself')

    const mounted = shown[0]!.el
    expect(mounted.isConnected).toBe(true)

    const close = mounted.querySelector('[data-close]')
    expect(close).not.toBeNull()

    close!.dispatchEvent(new MouseEvent('click'))
    // Marked immediately; removed once the exit transition has had its say.
    expect(mounted.getAttribute('data-exiting')).toBe('')

    await new Promise(resolve => setTimeout(resolve, 0))
    expect(mounted.isConnected).toBe(false)
  })
})

// Everything above stubs the toast host, so it pins what OUR code asks for and
// nothing about what the host does with it. This pins the third-party behaviour
// the sticky helper is built on: oat reads its duration through a destructuring
// default (`duration = 4000`), which fires only for `undefined`, so a 0 survives
// and its `if (duration > 0)` skips the dismiss timer entirely.
//
// Nothing in oat's README promises that. A release that switched to
// `options.duration || 4000` would give every terminal-close toast a
// four-second life -- the one message a user needs, because a fatal close
// schedules no retry -- and every stubbed test above would stay green.
describe('oat treats a zero duration as sticky', () => {
  beforeEach(() => {
    // jsdom implements neither popover method; oat's container calls both.
    for (const name of ['showPopover', 'hidePopover'] as const) {
      if (typeof HTMLElement.prototype[name] !== 'function') {
        Object.defineProperty(HTMLElement.prototype, name, {
          configurable: true,
          value() {},
        })
      }
    }
  })

  it('keeps a zero-duration toast and expires a positive-duration one', async () => {
    // Imported before the clock is faked: a dynamic import needs real timers.
    const { toastEl } = await import('@knadh/oat/js/toast.js')

    vi.useFakeTimers()
    try {
      const sticky = document.createElement('output')
      sticky.textContent = 'stays'
      const mountedSticky = toastEl(sticky, { placement: 'bottom-right', duration: 0 })

      const transient = document.createElement('output')
      transient.textContent = 'goes'
      const mountedTransient = toastEl(transient, { placement: 'bottom-right', duration: 50 })

      expect(mountedSticky?.isConnected).toBe(true)
      expect(mountedTransient?.isConnected).toBe(true)

      // Far past oat's own 4000 ms default, which is the value a regression
      // would substitute for the 0. Waiting a few milliseconds instead would
      // pass whether 0 meant "no timer" or "four seconds", which is exactly the
      // distinction this test exists to make.
      await vi.advanceTimersByTimeAsync(30_000)

      expect(mountedTransient?.isConnected).toBe(false)
      expect(mountedSticky?.isConnected).toBe(true)
    }
    finally {
      vi.useRealTimers()
    }
  })
})

// The design system installs `window.ot` during startup, so a toast raised
// before that runs finds no host. Every helper in this module is already
// reporting a failure, and a throw here would take down the handler that was
// explaining the first one -- `~/lib/clipboard` announces an unreachable
// clipboard from inside a click handler, which is exactly that shape.
describe('a missing toast host', () => {
  it('drops the message instead of throwing', () => {
    vi.stubGlobal('ot', undefined)

    expect(() => showWarnToast('a problem nobody will see')).not.toThrow()
    expect(shown).toHaveLength(0)
  })
})
