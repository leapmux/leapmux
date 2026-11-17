import type { Page } from '@playwright/test'
import { expect } from '@playwright/test'
import { test } from './fixtures'
import { expectNoVisibleHeightChange, framesPass, readRowBox, readRowSeqWithText, recordRowHeightChanges, widthSensitiveParagraphs } from './helpers/chatScrollStability'
import { sendScriptedTurn } from './helpers/scriptedTurn'
import { PHONE_METRICS } from './helpers/touch'
import { chatScrollContainer, waitForAgentStarted } from './helpers/ui'

/**
 * The chat view keeps a user row at its height while the age of the row ticks, on a phone.
 *
 * The toolbar of a user row sits beside the bubble and starts with the age of the message, such as `9s` or `1m`. A
 * wider age made the toolbar wider and the bubble narrower, so the text of the bubble wrapped again and the row
 * changed its height in view, with no input of the reader. On a phone the bubble fills the row, so a few pixels of
 * width wrap a line. The height key of a row holds no time, so a hidden copy that measured the row at another age gave
 * the row a wrong height too. The age now keeps one width at every age.
 *
 * The test sends one prompt of text that wraps at any character, so its line count follows the width of the bubble to
 * the pixel, and moves the page clock forward through ages of different widths.
 */

test.use(PHONE_METRICS)

/** The text of the prompt: paragraphs that each wrap at any character (see widthSensitiveParagraphs). */
const PROMPT_LABEL = 'Age'
const PROMPT = `Keep this prompt: ${widthSensitiveParagraphs(PROMPT_LABEL, 3)}`

/**
 * Each step of the clock, and the age that the toolbar of the row shows after it: two digits of seconds, then one
 * digit of minutes, then two. The ages of these three forms have three different widths in a proportional font.
 */
const AGE_STEPS = [
  { advanceMs: 10_000, age: /^\d{2}s$/ },
  { advanceMs: 50_000, age: /^1m$/ },
  { advanceMs: 9 * 60_000, age: /^1\dm$/ },
] as const

/**
 * The age that the toolbar of the row of `seq` shows (the text of the toolbar that has the form of an age, or '' for
 * none), the width of the toolbar, and the height of the row. Null while the row or its toolbar is not mounted.
 */
function readRowAge(page: Page, seq: string): Promise<{ age: string, toolbarWidth: number, rowHeight: number } | null> {
  return chatScrollContainer(page).evaluate((container, rowSeq) => {
    const row = container.querySelector(`[data-seq="${rowSeq}"]`)
    const toolbar = row?.querySelector('[data-testid="message-toolbar"]')
    if (row === null || row === undefined || toolbar === null || toolbar === undefined)
      return null
    let age = ''
    const walker = document.createTreeWalker(toolbar, NodeFilter.SHOW_TEXT)
    for (let node = walker.nextNode(); node !== null; node = walker.nextNode()) {
      const text = node.textContent?.trim() ?? ''
      if (/^\d+(?:[smhdy]|mo)$/.test(text))
        age = text
    }
    return {
      age,
      toolbarWidth: toolbar.getBoundingClientRect().width,
      rowHeight: row.getBoundingClientRect().height,
    }
  }, seq)
}

test.describe('chat scroll stability while the age of a row ticks', () => {
  test('keeps a user row and its toolbar at one size while the age of the row ticks, on a phone', async ({ page, authenticatedWorkspace, modelScript }) => {
    // The fake clock flows with real time until a step below moves it forward. The reload starts the app under it, so
    // the age of every row reads the one clock. This spec is in ISOLATED_CONTEXT_SPECS, because no API removes the clock
    // from its context.
    await page.clock.install()
    await page.reload()
    await waitForAgentStarted(page)
    const heights = await recordRowHeightChanges(page)
    await sendScriptedTurn(page, modelScript, { prompt: PROMPT, answer: 'Kept.', entry: 'insert' })
    const seq = await readRowSeqWithText(page, `${PROMPT_LABEL}0:`)
    expect(seq, 'the row of the prompt is mounted').not.toBeNull()
    const start = await readRowAge(page, seq!)
    expect(start, 'the row of the prompt has a toolbar').not.toBeNull()
    expect(start!.age, 'the age of a new row is a few seconds').toMatch(/^\d{1,2}s$/)

    for (const step of AGE_STEPS) {
      await page.clock.fastForward(step.advanceMs)
      await expect.poll(async () => (await readRowAge(page, seq!))?.age, { message: `the age of the row reaches ${step.age}` }).toMatch(step.age)
      // The row lays out the new age in the next frame.
      await framesPass(page, 2)
      const now = await readRowAge(page, seq!)
      expect(Math.abs(now!.rowHeight - start!.rowHeight), `the row keeps its height at the age ${now!.age}, within half a pixel (${start!.rowHeight}px at ${start!.age}, ${now!.rowHeight}px at ${now!.age})`).toBeLessThanOrEqual(0.5)
      // The toolbar sits beside the bubble, so a toolbar of another width wraps the bubble again.
      expect(Math.abs(now!.toolbarWidth - start!.toolbarWidth), `the toolbar keeps its width at the age ${now!.age}, within half a pixel (${start!.toolbarWidth}px at ${start!.age}, ${now!.toolbarWidth}px at ${now!.age})`).toBeLessThanOrEqual(0.5)
    }
    expect(await readRowBox(page, seq!), 'the row stays mounted').not.toBeNull()
    await expectNoVisibleHeightChange(heights, 'while the age of the row ticks')
  })
})
