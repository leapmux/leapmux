import type { CDPSession, Page } from '@playwright/test'
import { expect, test } from './fixtures'
import { focusActiveTerminal, waitForTerminalText } from './helpers/terminal'
import { openTerminalViaUI } from './helpers/ui'

/**
 * Input-method (IME) input in the terminal, driven through Chrome DevTools
 * Protocol so the browser generates GENUINE composition events rather than
 * synthesized ones. Playwright's keyboard cannot compose.
 *
 * Read this as a regression guard, not as the reproduction of the original
 * defect. Chromium composes correctly even without ~/lib/terminalIme; the
 * engine that drops and duplicates composed text is WebKit, which no headless
 * runner here can drive with a real input method. The reproduction lives in
 * src/lib/terminalIme.test.ts, which replays the WebKit trace from
 * xtermjs/xterm.js#5894 and asserts EXACT send sequences (`h.sent` equality).
 * What these specs prove is the other half: that taking the composition path
 * away from xterm did not break the engine that worked, end to end.
 */

/**
 * The exact bytes the active terminal's input gate forwarded, in order.
 *
 * The terminal buffer cannot carry a doubling assertion: the buffer-text hook
 * joins rows with no separator, so the shell's own echo of `echo 안녕` next to
 * the command's output puts `안녕안녕` in the string with exactly one send.
 * The gate's log is the send-side truth — every input path (xterm keys, IME
 * commits, programmatic writes) passes through it, so a doubled, lost, or
 * stray send is exact here whatever the shell redraws onto the screen.
 */
async function getInputLog(page: Page): Promise<string> {
  return page.evaluate(() => (window as any).__getActiveTerminalInputLog?.() ?? '')
}

async function expectInputLog(page: Page, expected: string): Promise<void> {
  const log = await getInputLog(page)
  expect(log, 'terminal input log').toBe(expected)
}

/**
 * One composition: the intermediate states, then the text it commits. A null
 * commit abandons the composition instead, which is what Escape does.
 */
interface Composition {
  steps: string[]
  commit: string | null
}

/**
 * Type through a real input method. Every keystroke an input method consumes
 * carries the legacy keyCode 229, which is what the terminal's IME layer keys
 * on to hold the keystroke back from xterm, so the specs send those too.
 */
async function composeInTerminal(cdp: CDPSession, compositions: Composition[]): Promise<void> {
  for (const { steps, commit } of compositions) {
    for (const text of steps) {
      await cdp.send('Input.dispatchKeyEvent', {
        type: 'rawKeyDown',
        windowsVirtualKeyCode: 229,
        nativeVirtualKeyCode: 229,
        key: 'Process',
      })
      await cdp.send('Input.imeSetComposition', {
        text,
        selectionStart: text.length,
        selectionEnd: text.length,
      })
      await cdp.send('Input.dispatchKeyEvent', { type: 'keyUp', key: 'Process' })
    }
    if (commit === null) {
      // Setting the composition to the empty string retracts it, which is how
      // Chromium reports a composition the user abandoned. `Input.insertText`
      // with an empty string is NOT the same thing and never resolves.
      await cdp.send('Input.imeSetComposition', { text: '', selectionStart: 0, selectionEnd: 0 })
      continue
    }
    await cdp.send('Input.insertText', { text: commit })
  }
}

/**
 * The preamble every test in this file starts with: open a terminal, wait for
 * it to render, and focus it so keystrokes — and the CDP-driven input method
 * — land in xterm.
 */
async function openFocusedTerminal(page: Page): Promise<void> {
  await openTerminalViaUI(page)
  await expect(page.locator('.xterm')).toBeVisible()
  await focusActiveTerminal(page)
}

test.describe('Terminal IME input', () => {
  test('composes Korean, where every syllable commits the previous one', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace
    await openFocusedTerminal(page)

    const cdp = await page.context().newCDPSession(page)
    // `echo` first so the assertion reads a command line the shell echoed back
    // -- xterm paints nothing on its own, so text on screen came from the PTY.
    await page.keyboard.type('echo ', { delay: 30 })
    // 안녕. 안 is committed by the keystroke that begins 녕, which is the case
    // that breaks: the commit and the next compositionstart arrive together.
    await composeInTerminal(cdp, [
      { steps: ['ㅇ', '아', '안'], commit: '안' },
      { steps: ['ㄴ', '녀', '녕'], commit: '녕' },
    ])
    await page.keyboard.press('Enter')

    await waitForTerminalText(page, '안녕')
    // One send of each syllable, once: the committed text and nothing else.
    await expectInputLog(page, 'echo 안녕\r')
  })

  test('composes Japanese with a single explicit commit', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace
    await openFocusedTerminal(page)

    const cdp = await page.context().newCDPSession(page)
    await page.keyboard.type('echo ', { delay: 30 })
    // One composition held open across several candidates, then committed --
    // the shape Japanese and Chinese input methods use, unlike Korean.
    await composeInTerminal(cdp, [
      { steps: ['こ', 'こん', 'こんに', 'こんにち', 'こんにちは'], commit: 'こんにちは' },
    ])
    await page.keyboard.press('Enter')

    await waitForTerminalText(page, 'こんにちは')
    await expectInputLog(page, 'echo こんにちは\r')
  })

  test('sends nothing when the composition is cancelled', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace
    await openFocusedTerminal(page)

    const cdp = await page.context().newCDPSession(page)
    await page.keyboard.type('echo CANCELLED', { delay: 30 })
    // Build a composition, then retract it the way Escape does.
    await composeInTerminal(cdp, [{ steps: ['ㅇ', '아', '안'], commit: null }])
    await page.keyboard.press('Enter')

    await waitForTerminalText(page, 'CANCELLED')
    // The abandoned syllable never left the client in any form: not the
    // intermediate jamo, not the composed syllable, not an erase for either.
    await expectInputLog(page, 'echo CANCELLED\r')
  })

  test('still types plain ASCII through xterm\'s own key path', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace
    await openFocusedTerminal(page)

    // The IME layer sits in front of every keystroke, so the ordinary path is
    // the regression that matters most.
    await page.keyboard.type('echo ASCII_STILL_WORKS', { delay: 30 })
    await page.keyboard.press('Enter')

    await waitForTerminalText(page, 'ASCII_STILL_WORKS')
    await expectInputLog(page, 'echo ASCII_STILL_WORKS\r')
  })
})
