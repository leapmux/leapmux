import type { Page } from '@playwright/test'
import { expect } from '@playwright/test'
import { withCleanup } from './cleanup'

interface ClipboardCaptureState {
  pending: number
  writes: string[]
  restore: () => void
}

type ClipboardCaptureWindow = Window & { __leapmuxClipboardCapture?: ClipboardCaptureState }

/** Install observers in the current document; this function must serialize without outer runtime values. */
function installClipboardCapture(): void {
  const host = window as ClipboardCaptureWindow
  if (host.__leapmuxClipboardCapture !== undefined)
    throw new Error('A clipboard capture is already active in this page.')
  const clipboard = navigator.clipboard
  const writeText = clipboard?.writeText
  const writeDescriptor = clipboard ? Object.getOwnPropertyDescriptor(clipboard, 'writeText') : undefined
  const execCommand = document.execCommand
  const execDescriptor = Object.getOwnPropertyDescriptor(document, 'execCommand')
  let writeInstalled = false
  let execInstalled = false
  const state: ClipboardCaptureState = {
    pending: 0,
    writes: [],
    restore: () => {
      if (writeInstalled) {
        if (writeDescriptor)
          Object.defineProperty(clipboard, 'writeText', writeDescriptor)
        else
          Reflect.deleteProperty(clipboard, 'writeText')
      }
      if (execInstalled) {
        if (execDescriptor)
          Object.defineProperty(document, 'execCommand', execDescriptor)
        else
          Reflect.deleteProperty(document, 'execCommand')
      }
      if (host.__leapmuxClipboardCapture === state)
        Reflect.deleteProperty(host, '__leapmuxClipboardCapture')
    },
  }
  try {
    if (clipboard && typeof writeText === 'function') {
      Object.defineProperty(clipboard, 'writeText', {
        configurable: true,
        writable: true,
        value: async (text: string) => {
          state.pending++
          try {
            await writeText.call(clipboard, text)
            state.writes.push(String(text))
          }
          finally {
            state.pending--
          }
        },
      })
      writeInstalled = true
    }
    if (typeof execCommand === 'function') {
      Object.defineProperty(document, 'execCommand', {
        configurable: true,
        writable: true,
        value: (...args: Parameters<Document['execCommand']>) => {
          let text: string | undefined
          if (String(args[0]).toLowerCase() === 'copy') {
            const element = document.activeElement
            if (element?.tagName === 'TEXTAREA' || element?.tagName === 'INPUT') {
              const input = element as HTMLInputElement | HTMLTextAreaElement
              text = input.value.slice(input.selectionStart ?? 0, input.selectionEnd ?? input.value.length)
            }
            else {
              text = window.getSelection()?.toString() ?? ''
            }
          }
          const copied = execCommand.apply(document, args)
          if (copied && text !== undefined)
            state.writes.push(text)
          return copied
        },
      })
      execInstalled = true
    }
    host.__leapmuxClipboardCapture = state
  }
  catch (error) {
    state.restore()
    throw error
  }
}

/**
 * Observe one successful native text copy in its owning page.
 * Parallel browsers share the OS clipboard, so a later global read can belong to another test.
 */
export async function captureClipboardText(page: Page, action: () => Promise<unknown>): Promise<string> {
  await page.context().grantPermissions(['clipboard-read', 'clipboard-write'])
  await page.evaluate(installClipboardCapture)
  return withCleanup(async () => {
    await action()
    if (page.isClosed())
      throw new Error('The copy page closed before its clipboard write could be verified.')
    await expect.poll(() => page.evaluate(() => {
      const state = (window as ClipboardCaptureWindow).__leapmuxClipboardCapture
      if (!state)
        throw new Error('No clipboard capture is active in this document.')
      return { pending: state.pending, copied: state.writes.length > 0 }
    })).toEqual({ pending: 0, copied: true })
    return page.evaluate(() => {
      const state = (window as ClipboardCaptureWindow).__leapmuxClipboardCapture
      if (!state || state.writes.length !== 1)
        throw new Error(`The Copy action completed ${state?.writes.length ?? 0} writes; expected exactly one.`)
      return state.writes[0]!
    })
  }, async () => {
    if (!page.isClosed())
      await page.evaluate(() => (window as ClipboardCaptureWindow).__leapmuxClipboardCapture?.restore())
  })
}
