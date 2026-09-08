import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { interceptUntrustedLinkClicks, UNTRUSTED_LINK_ATTRIBUTE } from './untrustedLinkClicks'

const openExternalUrl = vi.hoisted(() => vi.fn(async (_url: string) => {}))
vi.mock('~/api/platformBridge', () => ({ openExternalUrl }))

describe('interceptUntrustedLinkClicks', () => {
  let root: HTMLElement
  let detach: () => void
  const confirm = vi.fn(async () => true)

  /** An anchor under the listener, marked untrusted unless told otherwise. */
  function anchor(text: string, href: string, untrusted = true): HTMLAnchorElement {
    const el = document.createElement('a')
    el.href = href
    el.target = '_blank'
    el.textContent = text
    if (untrusted)
      el.setAttribute(UNTRUSTED_LINK_ATTRIBUTE, '')
    root.appendChild(el)
    return el
  }

  /** Click `el` and report whether the default action survived. */
  function click(el: HTMLElement, init: MouseEventInit = {}): boolean {
    return el.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, ...init }))
  }

  beforeEach(() => {
    openExternalUrl.mockClear()
    confirm.mockClear()
    confirm.mockResolvedValue(true)
    root = document.createElement('div')
    document.body.appendChild(root)
    detach = interceptUntrustedLinkClicks(root, confirm)
  })

  afterEach(() => {
    detach()
    root.remove()
  })

  it('leaves an honest link to the route that already opens it', async () => {
    // No preventDefault and no open of our own: under the desktop shell the
    // opener plugin's own listener takes it from here, and in a browser the
    // anchor does. Taking it over would only cost the click its user
    // activation, which is what a popup blocker looks for.
    const notCancelled = click(anchor('https://example.test/x', 'https://example.test/x'))

    expect(notCancelled).toBe(true)
    expect(confirm).not.toHaveBeenCalled()
    expect(openExternalUrl).not.toHaveBeenCalled()
  })

  it('asks first when the text names a different address, then opens it', async () => {
    const notCancelled = click(anchor('https://good.example', 'https://evil.example/steal'))
    await vi.waitFor(() => expect(openExternalUrl).toHaveBeenCalled())

    // Cancelled, which is also what disengages the opener plugin's listener:
    // it returns immediately on `event.defaultPrevented`.
    expect(notCancelled).toBe(false)
    expect(confirm).toHaveBeenCalledWith(expect.objectContaining({
      uri: 'https://evil.example/steal',
      label: 'https://good.example',
      misleadingLabel: 'https://good.example',
    }))
    expect(openExternalUrl).toHaveBeenCalledWith('https://evil.example/steal')
  })

  it('opens nothing when the user refuses', async () => {
    confirm.mockResolvedValue(false)

    click(anchor('Click here', 'https://evil.example/'))
    await vi.waitFor(() => expect(confirm).toHaveBeenCalled())

    expect(openExternalUrl).not.toHaveBeenCalled()
  })

  it('takes the same route for a modified click and a middle click', async () => {
    // Each one is another way to open the link, so each one is another way
    // around the prompt. A browser reports the middle button as `auxclick`.
    const el = anchor('https://good.example', 'https://evil.example/')

    expect(click(el, { metaKey: true })).toBe(false)
    expect(click(el, { ctrlKey: true })).toBe(false)
    expect(el.dispatchEvent(new MouseEvent('auxclick', { bubbles: true, cancelable: true, button: 1 }))).toBe(false)
    await vi.waitFor(() => expect(confirm).toHaveBeenCalledTimes(3))
  })

  it('blocks a scheme no click may open, and opens nothing', async () => {
    const notCancelled = click(anchor('passwords', 'file:///etc/passwd'))

    expect(notCancelled).toBe(false)
    expect(confirm).not.toHaveBeenCalled()
    expect(openExternalUrl).not.toHaveBeenCalled()
  })

  it('ignores an anchor the app itself wrote', async () => {
    // `AboutDialog`'s licence link reads as prose over a leapmux.dev address --
    // an honest label and a mismatch at once. First-party copy is not a
    // deception risk, so the mark is opt-IN.
    const notCancelled = click(anchor('Functional Source License', 'https://leapmux.dev/legal/', false))

    expect(notCancelled).toBe(true)
    expect(confirm).not.toHaveBeenCalled()
  })

  it('leaves a click that something else already claimed', async () => {
    // `defaultPrevented` means another handler owns this click. Taking it over
    // anyway would open a link the app had already decided not to follow.
    const claimed = document.createElement('div')
    document.body.appendChild(claimed)
    claimed.addEventListener('click', event => event.preventDefault(), { capture: true })
    const stop = interceptUntrustedLinkClicks(claimed, confirm)
    const el = document.createElement('a')
    el.href = 'https://evil.example/'
    el.textContent = 'https://good.example'
    el.setAttribute(UNTRUSTED_LINK_ATTRIBUTE, '')
    claimed.appendChild(el)

    el.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))

    expect(confirm).not.toHaveBeenCalled()
    expect(openExternalUrl).not.toHaveBeenCalled()
    stop()
    claimed.remove()
  })

  it('resolves a relative address the way the click would', async () => {
    // `href` is read as the resolved property, never as the raw attribute, so
    // the policy judges the address the browser would actually open.
    const el = anchor('docs', '/NOTICE.html')

    expect(click(el)).toBe(false)
    await vi.waitFor(() => expect(confirm).toHaveBeenCalled())
    expect(confirm).toHaveBeenCalledWith(expect.objectContaining({
      uri: `${window.location.origin}/NOTICE.html`,
    }))
  })

  it('stops listening once detached', async () => {
    const el = anchor('https://good.example', 'https://evil.example/')
    detach()

    expect(click(el)).toBe(true)
    expect(confirm).not.toHaveBeenCalled()
  })
})
