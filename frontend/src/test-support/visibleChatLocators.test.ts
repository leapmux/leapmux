import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { collectE2EFiles, e2eRoot } from '~/test-support/e2eFiles'
import { frontendRoot, posixRelative } from '~/test-support/sourceTree'

// E2E guard: a page-rooted chat locator must be scoped to what the user can
// SEE. ChatView keeps a hidden premeasure copy of every row whose height is
// still unknown -- same test ids, same text, `visibility: hidden` -- so a bare
// `page.locator('[data-testid="message-bubble"]')` transiently matches twice
// per message and Playwright's strict mode fails the assertion outright. The
// window is ~20ms on an idle box and much wider under the full suite's
// concurrency, which is why this read as "flaky only at high worker counts".
//
// The helpers in tests/e2e/helpers/ui.ts (assistantBubbles, userBubbles,
// messageBubbles, messageContents, visibleOnly) are already scoped. This fails
// the suite if a spec goes back to hand-writing an unscoped one, since the
// resulting flake is rare enough to survive several green runs.
//
// Only the OUTERMOST locator needs the filter: anything scoped under an
// already-visible bubble cannot be in the premeasure root, so
// `bubble.locator('[data-testid="message-content"]')` is fine and is not
// matched by the pattern below.

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
 * `page.locator('text=...')`, the legacy text ENGINE, rooted at the page.
 *
 * A test id is not the only way in. A text locator matches the cloned row's
 * content exactly as it matches the real row's, and `050-plan-mode.spec.ts`
 * failed on `page.locator('text=Context cleared')` resolving to two elements
 * while the pattern above let it through. Playwright does not retry a
 * strict-mode violation, so it failed in five seconds rather than waiting for
 * the row to settle.
 *
 * The text ENGINE is flagged and `getByText` is not. The suite roots over a
 * hundred `getByText` calls at the page, nearly all of them on auth pages, the
 * sidebar and dialogs, which ChatView never clones; flagging them would teach a
 * mechanical `:visible` rather than catch a defect. The engine form is rare,
 * Playwright discourages it in favour of `getByText`, and the one site that used
 * it was chat content. A page-rooted `getByText` on chat content is therefore
 * NOT caught here -- scope it with `visibleOnly` by hand.
 */
const UNSCOPED_TEXT_ENGINE = /page\s*\.\s*locator\(\s*(['`])text=[^'`]*\1\)(?!\s*\.\s*filter\(\s*\{\s*visible\s*:\s*true)/g

describe('e2e chat locators', () => {
  it('never roots an unscoped chat locator at the page', () => {
    const offenders: string[] = []
    for (const file of collectE2EFiles()) {
      // ui.ts is where the scoped helpers are DEFINED, so it holds the only
      // legitimate occurrences of the raw selectors.
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
    }
    const hint = [
      'Page-rooted chat locators match ChatView\'s hidden premeasure copy as well as the real row,',
      'which fails Playwright strict mode at random. Use the scoped helpers in tests/e2e/helpers/ui.ts',
      '(assistantBubbles / userBubbles / messageBubbles / messageContents / visibleOnly):',
    ].join(' ')
    expect(offenders, `${hint}\n  ${offenders.join('\n  ')}`).toEqual([])
  })
})
