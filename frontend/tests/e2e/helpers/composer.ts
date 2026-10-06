import type { Locator, Page } from '@playwright/test'
import { composerEditor } from './ui'

/**
 * Input to the composer editor, and reads of its content, for the E2E specs.
 * `./ui.ts` holds the composer locators (`composerEditor`, `focusComposer`) and the send (`sendMessage`).
 */

/**
 * Paste `text` as plain text into the visible composer, through a synthetic `paste` event.
 * The event carries the text in a `DataTransfer` of its own, so the paste reads no system clipboard and needs no
 * clipboard permission. The editor handles the event as it handles a real paste: its paste plugin reads the plain text.
 */
export async function pasteText(page: Page, text: string): Promise<void> {
  await composerEditor(page).evaluate((editor, pasted) => {
    const data = new DataTransfer()
    data.setData('text/plain', pasted)
    editor.dispatchEvent(new ClipboardEvent('paste', { clipboardData: data, bubbles: true, cancelable: true }))
  }, text)
}

/**
 * Read the text of the one code block in `editor`, without its language label.
 * The editor draws the label as a widget inside the `<code>` element, so the plain text of the element starts with
 * the label. The read is not retried: assert through `expect.poll(() => codeBlockText(editor))`, because a paste
 * reaches the document after the event returns.
 */
export async function codeBlockText(editor: Locator): Promise<string> {
  return editor.locator('pre code').evaluate((code) => {
    const clone = code.cloneNode(true) as HTMLElement
    clone.querySelectorAll('.code-lang-label').forEach(label => label.remove())
    return clone.textContent ?? ''
  })
}
