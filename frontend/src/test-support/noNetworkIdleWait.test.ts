import { readFileSync } from 'node:fs'
import { dirname, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { collectE2EFiles } from '~/test-support/e2eFiles'
import { stripCommentLines } from '~/test-support/sourceScan'

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

/**
 * The string literal, in any quoting style. That covers
 * `waitForLoadState('networkidle')`, `waitUntil: 'networkidle'`, and any
 * other spelling that puts the word in an argument position.
 */
const NETWORK_IDLE = /(['"`])networkidle\1/

describe('e2e load-state waits', () => {
  it('never waits for networkidle', () => {
    const offenders: string[] = []
    for (const file of collectE2EFiles()) {
      // The ban has to be EXPLAINED somewhere, and the explanation identifies the
      // call, so the comment lines go before the scan reads them.
      const lines = stripCommentLines(readFileSync(file, 'utf-8')).split('\n')
      lines.forEach((text, index) => {
        if (!NETWORK_IDLE.test(text))
          return
        offenders.push(`${relative(frontendRoot, file)}:${index + 1}  ${text.trim()}`)
      })
    }
    const hint = [
      'A page that solved an ALTCHA captcha never reaches networkidle: altcha terminates its',
      'solver workers mid-fetch and those requests stay in-flight forever, so the wait burns the',
      'whole 300s test budget. Wait for a locator the app renders instead:',
    ].join(' ')
    // The case above passes vacuously if the walk returns nothing, so
    // `e2eFiles.test.ts` pins that it does not -- once, for the three guards
    // that share it.
    expect(offenders, `${hint}\n  ${offenders.join('\n  ')}`).toEqual([])
  })
})
