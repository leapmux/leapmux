import type { Page } from '@playwright/test'
import { runInNewContext } from 'node:vm'

/** Execute serialized page functions while two actors share one simulated OS clipboard. */
export function createClipboardPageFixture() {
  const events: string[] = []
  const nativeWrites: string[] = []
  let clipboardText = ''
  let writeFailure: Error | undefined
  let legacySucceeds = true
  let closed = false
  const originalWriteText = async (text: string) => {
    if (writeFailure)
      throw writeFailure
    nativeWrites.push(text)
    clipboardText = text
  }
  const clipboard = {
    writeText: originalWriteText,
    readText: async () => clipboardText,
  }
  const navigator = { clipboard: clipboard as typeof clipboard | undefined }
  const document = {
    activeElement: {
      tagName: 'TEXTAREA',
      value: '',
      selectionStart: 0,
      selectionEnd: 0,
    },
    execCommand: (command: string) => {
      if (command.toLowerCase() !== 'copy' || !legacySucceeds)
        return false
      const selection = document.activeElement
      clipboardText = selection.value.slice(selection.selectionStart, selection.selectionEnd)
      nativeWrites.push(clipboardText)
      return true
    },
  }
  const realm = {
    navigator,
    document,
    window: {
      getSelection: () => null,
    },
    __clipboardArgument: undefined as unknown,
  }
  const page = {
    context: () => ({
      grantPermissions: async (permissions: string[]) => { events.push(`grant:${permissions.join(',')}`) },
    }),
    isClosed: () => closed,
    evaluate: async (operation: ((argument: unknown) => unknown) | string, argument?: unknown) => {
      if (closed)
        throw new Error('Page is closed.')
      realm.__clipboardArgument = argument
      const source = typeof operation === 'string'
        ? operation
        : `(${operation.toString()})(__clipboardArgument)`
      return runInNewContext(source, realm, { timeout: 1000 })
    },
  } as unknown as Page
  return {
    page,
    events,
    nativeWrites,
    clipboard,
    navigator,
    document,
    originalWriteText,
    text: () => clipboardText,
    copy: async (text: string) => {
      if (!navigator.clipboard)
        throw new Error('Clipboard API is absent.')
      await navigator.clipboard.writeText(text)
    },
    overwriteFromAnotherPage: originalWriteText,
    selectLegacyText: (text: string, start = 0, end = text.length) => {
      document.activeElement = {
        tagName: 'TEXTAREA',
        value: text,
        selectionStart: start,
        selectionEnd: end,
      }
    },
    failWrites: (error?: Error) => { writeFailure = error },
    allowLegacyCopy: (allow: boolean) => { legacySucceeds = allow },
    close: () => { closed = true },
  }
}
