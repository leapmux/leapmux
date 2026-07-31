import { readdirSync, readFileSync } from 'node:fs'
import { dirname, join, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

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
  'message-error',
  'message-pending',
  'message-retry-button',
  'message-delete-button',
]

const frontendRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..', '..')
const e2eRoot = join(frontendRoot, 'tests', 'e2e')

/** `page.locator('[data-testid="<chat id>"...]')` without a `:visible` filter. */
const UNSCOPED = new RegExp(
  `page\\s*\\.\\s*locator\\(\\s*(['\`])\\[data-testid="(?:${CHAT_TEST_IDS.join('|')})"\\][^'\`]*\\1`,
  'g',
)

function collectSpecFiles(dir: string): string[] {
  const found: string[] = []
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name)
    if (entry.isDirectory())
      found.push(...collectSpecFiles(full))
    else if (entry.name.endsWith('.ts'))
      found.push(full)
  }
  return found
}

describe('e2e chat locators', () => {
  it('never roots an unscoped chat locator at the page', () => {
    const offenders: string[] = []
    for (const file of collectSpecFiles(e2eRoot)) {
      // ui.ts is where the scoped helpers are DEFINED, so it holds the only
      // legitimate occurrences of the raw selectors.
      if (relative(e2eRoot, file) === join('helpers', 'ui.ts'))
        continue
      const source = readFileSync(file, 'utf-8')
      for (const match of source.matchAll(UNSCOPED)) {
        if (match[0].includes(':visible'))
          continue
        const line = source.slice(0, match.index).split('\n').length
        offenders.push(`${relative(frontendRoot, file)}:${line}  ${match[0]}`)
      }
    }
    const hint = [
      'Page-rooted chat locators match ChatView\'s hidden premeasure copy as well as the real row,',
      'which fails Playwright strict mode at random. Use the scoped helpers in tests/e2e/helpers/ui.ts',
      '(assistantBubbles / userBubbles / messageBubbles / messageContents / visibleOnly):',
    ].join(' ')
    expect(offenders, `${hint}\n  ${offenders.join('\n  ')}`).toEqual([])
  })
})
