import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { collectE2EFiles, e2eRoot } from '~/test-support/e2eFiles'
import { frontendRoot, posixRelative } from '~/test-support/sourceTree'

// End-to-end (E2E) chat locators must select the visible row.
// ChatView creates a hidden premeasure copy of each row with an unknown height.
// Both copies contain the same test IDs and text.
// An unscoped locator can match both copies and fail Playwright strict mode.
// The duplicate can remain for about 20ms on an idle host. Load can extend that interval.
// The scoped helpers in tests/e2e/helpers/ui.ts select the visible copy:
// - assistantBubbles.
// - userBubbles.
// - messageBubbles.
// - messageContents.
// - visibleOnly.
// This guard rejects an unscoped locator before the intermittent browser failure occurs.
// Apply the filter to the outermost locator. Descendants of a visible bubble need no second filter.
// The scan permits bubble.locator('[data-testid="message-content"]') for that reason.

const CHAT_TEST_IDS = [
  'message-bubble',
  'message-content',
]

/** `page.locator('[data-testid="<chat id>"...]')` without a `:visible` filter. */
const UNSCOPED = new RegExp(
  `page\\s*\\.\\s*locator\\(\\s*(['\`])\\[data-testid="(?:${CHAT_TEST_IDS.join('|')})"\\][^'\`]*\\1`,
  'g',
)

/**
 * Find a legacy text locator that starts at the page root.
 *
 * A text locator can match the hidden premeasure copy and the visible row.
 * The original Plan mode case failed because text=Context cleared matched both copies.
 * Its current path is tests/e2e/claude-code/plan-approval-banner.spec.ts.
 * The test-ID pattern did not catch that text locator.
 * Playwright rejects a strict-mode violation without a retry, even when the duplicate disappears shortly afterward.
 *
 * This guard checks the legacy text engine. It does not check getByText.
 * Most page-root getByText calls select authentication pages, sidebars, or dialogs.
 * ChatView creates no premeasure copy of those surfaces.
 * Rejecting all those calls would require filters without finding a chat defect.
 * Playwright discourages the legacy text engine, and its previous unscoped use selected chat content.
 * A page-root getByText call that selects chat content still needs visibleOnly.
 */
const UNSCOPED_TEXT_ENGINE = /page\s*\.\s*locator\(\s*(['`])text=[^'`]*\1\)(?!\s*\.\s*filter\(\s*\{\s*visible\s*:\s*true)/g

/** A startup overlay belongs to the same visible ChatView as its composer. */
const UNSCOPED_STARTUP_OVERLAY = /page\s*\.\s*getByTestId\(\s*(['"`])agent-startup-overlay\1\s*\)(?!\s*\.\s*filter\(\s*\{\s*visible\s*:\s*true)/g

describe('e2e chat locators', () => {
  it('detects an unscoped startup overlay while allowing visible scoping', () => {
    expect('context.page.getByTestId(\'agent-startup-overlay\')'.match(UNSCOPED_STARTUP_OVERLAY)).toHaveLength(1)
    expect('context.page.getByTestId(\'agent-startup-overlay\').filter({ visible: true })'.match(UNSCOPED_STARTUP_OVERLAY)).toBeNull()
    const scoped = 'visibleOnly(context.page.getByTestId(\'agent-startup-overlay\'))'
    const match = [...scoped.matchAll(UNSCOPED_STARTUP_OVERLAY)][0]
    expect(match).toBeDefined()
    if (!match)
      throw new Error('The startup scope sample contains no test-ID locator.')
    expect(/visibleOnly\(\s*(?:[A-Za-z_$][\w$]*\.)?$/.test(scoped.slice(0, match.index))).toBe(true)
  })

  it('never roots an unscoped chat locator at the page', () => {
    const offenders: string[] = []
    for (const file of collectE2EFiles()) {
      // helpers/ui.ts defines the scoped helpers and contains their raw selectors.
      if (posixRelative(e2eRoot, file) === 'helpers/ui.ts')
        continue
      const source = readFileSync(file, 'utf-8')
      const report = (match: RegExpMatchArray) => {
        const line = source.slice(0, match.index).split('\n').length
        offenders.push(`${posixRelative(frontendRoot, file)}:${line}  ${match[0]}`)
      }
      for (const match of source.matchAll(UNSCOPED)) {
        if (match[0].includes(':visible'))
          continue
        report(match)
      }
      for (const match of source.matchAll(UNSCOPED_TEXT_ENGINE))
        report(match)
      for (const match of source.matchAll(UNSCOPED_STARTUP_OVERLAY)) {
        const before = source.slice(0, match.index)
        if (/visibleOnly\(\s*(?:[A-Za-z_$][\w$]*\.)?$/.test(before))
          continue
        report(match)
      }
    }
    const hint = [
      'A page-root chat locator can match the hidden premeasure copy and the visible row.',
      'That duplicate causes an intermittent Playwright strict-mode failure.',
      'Use the scoped chat helpers in tests/e2e/helpers/ui.ts:',
    ].join(' ')
    expect(offenders, `${hint}\n  ${offenders.join('\n  ')}`).toEqual([])
  })
})
