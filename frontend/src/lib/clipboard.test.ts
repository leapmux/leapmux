import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { copyTextToClipboard } from './clipboard'

const showWarnToastWithLoggedCause = vi.hoisted(() => vi.fn())
vi.mock('~/components/common/Toast', () => ({ showWarnToastWithLoggedCause }))

function stubClipboard(value: unknown) {
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value })
}

/**
 * jsdom implements no `execCommand`, so the fallback path has to be stated.
 *
 * Returns the spy AND the text the browser would have copied, read off the
 * textarea that is selected at the moment of the call -- which is the only
 * evidence that the fallback selected the right thing. Reading it afterwards
 * proves nothing, because the helper removes the element in its `finally`.
 */
function stubExecCommand(succeeds: boolean) {
  const copied: string[] = []
  const execCommand = vi.fn((command: string) => {
    if (command !== 'copy')
      return false
    const active = document.activeElement
    if (active instanceof HTMLTextAreaElement)
      copied.push(active.value)
    return succeeds
  })
  Object.defineProperty(document, 'execCommand', { configurable: true, value: execCommand })
  return { execCommand, copied }
}

function setSecureContext(value: boolean | undefined) {
  Object.defineProperty(window, 'isSecureContext', { configurable: true, value })
}

const originalSecureContext = Object.getOwnPropertyDescriptor(window, 'isSecureContext')

beforeEach(() => {
  vi.clearAllMocks()
})

afterEach(() => {
  Reflect.deleteProperty(navigator, 'clipboard')
  Reflect.deleteProperty(document, 'execCommand')
  if (originalSecureContext)
    Object.defineProperty(window, 'isSecureContext', originalSecureContext)
  else
    Reflect.deleteProperty(window, 'isSecureContext')
  document.body.replaceChildren()
})

describe('copyTextToClipboard', () => {
  it('writes non-empty text to the clipboard', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    stubClipboard({ writeText })

    await expect(copyTextToClipboard('hello')).resolves.toBe(true)

    expect(writeText).toHaveBeenCalledWith('hello')
    expect(showWarnToastWithLoggedCause).not.toHaveBeenCalled()
  })

  it('skips empty strings (avoids clobbering the clipboard on deselect)', async () => {
    const writeText = vi.fn()
    stubClipboard({ writeText })
    const { execCommand } = stubExecCommand(true)

    await expect(copyTextToClipboard('')).resolves.toBe(false)

    expect(writeText).not.toHaveBeenCalled()
    // An empty input is not a failure: nothing was asked for, so nothing is
    // attempted and the user is told nothing.
    expect(execCommand).not.toHaveBeenCalled()
    expect(showWarnToastWithLoggedCause).not.toHaveBeenCalled()
  })

  it('does not reach the fallback when the Clipboard API succeeds', async () => {
    stubClipboard({ writeText: vi.fn().mockResolvedValue(undefined) })
    const { execCommand } = stubExecCommand(true)

    await expect(copyTextToClipboard('hello')).resolves.toBe(true)

    expect(execCommand).not.toHaveBeenCalled()
  })

  describe('the execCommand fallback', () => {
    // A non-secure origin -- plain http:// on a LAN, which is how the app is
    // read on a phone -- exposes no `navigator.clipboard` at all. The fallback
    // is the only path that copies anything there.
    it('copies when the platform exposes no clipboard', async () => {
      stubClipboard(undefined)
      const { copied } = stubExecCommand(true)

      await expect(copyTextToClipboard('hello')).resolves.toBe(true)

      expect(copied).toEqual(['hello'])
      expect(showWarnToastWithLoggedCause).not.toHaveBeenCalled()
    })

    it('copies when the Clipboard API rejects', async () => {
      stubClipboard({ writeText: vi.fn().mockRejectedValue(new Error('denied')) })
      const { copied } = stubExecCommand(true)

      await expect(copyTextToClipboard('hello')).resolves.toBe(true)

      expect(copied).toEqual(['hello'])
    })

    it('leaves no textarea behind', async () => {
      stubClipboard(undefined)
      stubExecCommand(true)

      await copyTextToClipboard('hello')

      expect(document.querySelector('textarea')).toBeNull()
    })

    it('removes the textarea even when execCommand throws', async () => {
      stubClipboard(undefined)
      Object.defineProperty(document, 'execCommand', {
        configurable: true,
        value: vi.fn(() => {
          throw new Error('unsupported')
        }),
      })

      await expect(copyTextToClipboard('hello')).resolves.toBe(false)

      expect(document.querySelector('textarea')).toBeNull()
    })

    // The quote popover reads the same highlight again straight after the copy,
    // and both file views keep one on screen while the button is pressed.
    it('puts the document selection back', async () => {
      stubClipboard(undefined)
      stubExecCommand(true)
      const prose = document.createElement('p')
      prose.append(document.createTextNode('some selectable prose'))
      document.body.appendChild(prose)
      const range = document.createRange()
      range.setStart(prose.firstChild!, 5)
      range.setEnd(prose.firstChild!, 15)
      const selection = window.getSelection()!
      selection.removeAllRanges()
      selection.addRange(range)

      await copyTextToClipboard('hello')

      expect(selection.toString()).toBe('selectable')
    })

    it('gives the focus back to whatever held it', async () => {
      stubClipboard(undefined)
      stubExecCommand(true)
      const input = document.createElement('input')
      document.body.appendChild(input)
      input.focus()

      await copyTextToClipboard('hello')

      expect(document.activeElement).toBe(input)
    })

    it('reports failure when execCommand declines', async () => {
      stubClipboard(undefined)
      stubExecCommand(false)

      await expect(copyTextToClipboard('hello')).resolves.toBe(false)
    })

    // Neither path exists: no `navigator.clipboard`, and no `execCommand` to
    // fall back to. This is the shape of the original defect -- every copy in
    // the app returned `false` and said nothing -- so it must stay covered.
    it('reports failure and announces it when neither path exists', async () => {
      stubClipboard(undefined)

      await expect(copyTextToClipboard('hello')).resolves.toBe(false)

      expect(document.querySelector('textarea')).toBeNull()
      expect(showWarnToastWithLoggedCause).toHaveBeenCalledTimes(1)
    })

    // `readOnly` keeps iOS from raising the on-screen keyboard for a textarea
    // the user never sees, and the off-screen style keeps the page from
    // scrolling to it. Both are load-bearing on the phone this path exists for,
    // and neither is visible in the copied text, so only an assertion holds them.
    it('selects a read-only textarea that is off the screen', async () => {
      stubClipboard(undefined)
      const seen: Array<{ readOnly: boolean, position: string, opacity: string }> = []
      Object.defineProperty(document, 'execCommand', {
        configurable: true,
        value: vi.fn(() => {
          const active = document.activeElement
          if (active instanceof HTMLTextAreaElement) {
            seen.push({
              readOnly: active.readOnly,
              position: active.style.position,
              opacity: active.style.opacity,
            })
          }
          return true
        }),
      })

      await copyTextToClipboard('hello')

      expect(seen).toEqual([{ readOnly: true, position: 'fixed', opacity: '0' }])
    })

    // Firefox is the engine that hands out more than one range. The restore
    // loops, so a second range must come back too rather than being dropped.
    it('puts every range back, not only the first', async () => {
      stubClipboard(undefined)
      stubExecCommand(true)
      const prose = document.createElement('p')
      prose.append(document.createTextNode('alpha bravo'))
      document.body.appendChild(prose)
      const text = prose.firstChild!
      const first = document.createRange()
      first.setStart(text, 0)
      first.setEnd(text, 5)
      const second = document.createRange()
      second.setStart(text, 6)
      second.setEnd(text, 11)
      const addRange = vi.fn()
      vi.spyOn(window, 'getSelection').mockReturnValue({
        rangeCount: 2,
        getRangeAt: (index: number) => (index === 0 ? first : second),
        removeAllRanges: () => {},
        addRange,
      } as unknown as Selection)

      await copyTextToClipboard('hello')

      expect(addRange).toHaveBeenCalledTimes(2)
      expect(addRange.mock.calls[0]![0].toString()).toBe('alpha')
      expect(addRange.mock.calls[1]![0].toString()).toBe('bravo')
    })

    it('copies text far longer than one line', async () => {
      stubClipboard(undefined)
      const { copied } = stubExecCommand(true)
      const long = 'x'.repeat(100_000)

      await expect(copyTextToClipboard(long)).resolves.toBe(true)

      expect(copied[0]).toHaveLength(100_000)
    })

    // The contract is "never rejects and never throws", and callers rely on it
    // with a bare `void`. Reading the selection happens before the copy, so a
    // `Selection` that refuses -- `getRangeAt` raises once the count it was
    // asked about has changed under a still-moving finger -- must not escape.
    it('still copies when the selection refuses to be read', async () => {
      stubClipboard(undefined)
      const { copied } = stubExecCommand(true)
      vi.spyOn(window, 'getSelection').mockReturnValue({
        rangeCount: 1,
        getRangeAt: () => {
          throw new Error('IndexSizeError')
        },
        removeAllRanges: () => {},
        addRange: () => {},
      } as unknown as Selection)

      await expect(copyTextToClipboard('hello')).resolves.toBe(true)

      expect(copied).toEqual(['hello'])
      expect(document.querySelector('textarea')).toBeNull()
    })

    it('does not turn a landed copy into a throw when the restore refuses', async () => {
      stubClipboard(undefined)
      stubExecCommand(true)
      vi.spyOn(window, 'getSelection').mockReturnValue({
        rangeCount: 0,
        getRangeAt: () => {
          throw new Error('unreachable')
        },
        removeAllRanges: () => {
          throw new Error('refused')
        },
        addRange: () => {},
      } as unknown as Selection)

      await expect(copyTextToClipboard('hello')).resolves.toBe(true)

      expect(document.querySelector('textarea')).toBeNull()
    })
  })

  describe('the failure it announces', () => {
    it('names the insecure page when the browser withheld the clipboard', async () => {
      setSecureContext(false)
      stubClipboard(undefined)
      stubExecCommand(false)

      await expect(copyTextToClipboard('hello')).resolves.toBe(false)

      expect(showWarnToastWithLoggedCause).toHaveBeenCalledTimes(1)
      expect(showWarnToastWithLoggedCause.mock.calls[0]![0]).toContain('insecure page')
    })

    // A secure page that still cannot copy is a refusal -- a denied permission,
    // or a document that does not hold focus -- and telling that user to switch
    // to HTTPS would send them after the wrong thing.
    it('names a refusal when the page is secure', async () => {
      setSecureContext(true)
      stubClipboard({ writeText: vi.fn().mockRejectedValue(new Error('NotAllowedError')) })
      stubExecCommand(false)

      await copyTextToClipboard('hello')

      expect(showWarnToastWithLoggedCause.mock.calls[0]![0]).toContain('refused')
    })

    // jsdom leaves `isSecureContext` undefined, and an unknown context must not
    // accuse the page of being insecure.
    it('does not accuse an unknown context of being insecure', async () => {
      setSecureContext(undefined)
      stubClipboard(undefined)
      stubExecCommand(false)

      await copyTextToClipboard('hello')

      expect(showWarnToastWithLoggedCause.mock.calls[0]![0]).toContain('refused')
    })

    it('logs the rejection as the cause', async () => {
      const denied = new Error('denied')
      stubClipboard({ writeText: vi.fn().mockRejectedValue(denied) })
      stubExecCommand(false)

      await copyTextToClipboard('hello')

      expect(showWarnToastWithLoggedCause.mock.calls[0]![1]).toBe(denied)
    })

    it('stays silent when the caller declines the announcement', async () => {
      stubClipboard(undefined)
      stubExecCommand(false)

      await expect(copyTextToClipboard('hello', { announceFailure: false })).resolves.toBe(false)

      expect(showWarnToastWithLoggedCause).not.toHaveBeenCalled()
    })
  })
})
