import { readdirSync, readFileSync } from 'node:fs'
import { dirname, join, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

// E2E guard: no spec or helper may wait for `networkidle`.
//
// The app can NEVER reach it once a page solves an ALTCHA captcha, which
// every login does. altcha's solveChallengeWorkers spawns
// `min(16, navigator.hardwareConcurrency)` workers that all fetch the same
// solver chunk at once, and its `finally` terminates every one of them the
// moment the first returns a solution. Chromium drops the script loads of the
// workers that were still fetching, and Playwright's
// CRNetworkManager.removeSession discards that session's listeners without
// failing those requests -- so they stay in `frame._inflightRequests` for the
// rest of the page's life and the idle timer never starts.
//
// The failure is silent and expensive: `navigationTimeout` is unset (0 = no
// limit), so the wait runs to the full 300s test budget and reports a bare
// "Test timeout" that names no assertion. 179's SCRYPT login died exactly
// that way, with seven of ten workers still loading their chunk when the
// 1 MiB-per-derivation solve finished.
//
// It is also a race, not a constant: a slower algorithm (ARGON2ID at 8 MiB
// per derivation) usually lets every worker finish loading first and passes.
// So this cannot be left to the suite to catch.
//
// Wait for what the app SHOWS instead -- `loginViaUI` waits for the app
// shell's own menu trigger. Playwright documents `networkidle` as
// discouraged for exactly this class of reason.

const frontendRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..', '..')
const e2eRoot = join(frontendRoot, 'tests', 'e2e')

/**
 * The string literal, in any quoting style. That covers
 * `waitForLoadState('networkidle')`, `waitUntil: 'networkidle'`, and any
 * other spelling that puts the word in an argument position.
 */
const NETWORK_IDLE = /(['"`])networkidle\1/

/**
 * A line whose first non-space characters open or continue a comment. The
 * ban has to be EXPLAINED somewhere, and the explanation has to name the
 * call, so a comment that quotes it is not an offence. A trailing comment on
 * a line of code still counts -- move the note above the line.
 */
const COMMENT_LINE = /^\s*(?:\/\/|\/\*|\*)/

function collectE2EFiles(dir: string): string[] {
  const found: string[] = []
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name)
    if (entry.isDirectory())
      found.push(...collectE2EFiles(full))
    else if (entry.name.endsWith('.ts'))
      found.push(full)
  }
  return found
}

describe('e2e load-state waits', () => {
  it('never waits for networkidle', () => {
    const offenders: string[] = []
    for (const file of collectE2EFiles(e2eRoot)) {
      const lines = readFileSync(file, 'utf-8').split('\n')
      lines.forEach((text, index) => {
        if (COMMENT_LINE.test(text) || !NETWORK_IDLE.test(text))
          return
        offenders.push(`${relative(frontendRoot, file)}:${index + 1}  ${text.trim()}`)
      })
    }
    const hint = [
      'A page that solved an ALTCHA captcha never reaches networkidle: altcha terminates its',
      'solver workers mid-fetch and those requests stay in-flight forever, so the wait burns the',
      'whole 300s test budget. Wait for a locator the app renders instead:',
    ].join(' ')
    expect(offenders, `${hint}\n  ${offenders.join('\n  ')}`).toEqual([])
  })
})
