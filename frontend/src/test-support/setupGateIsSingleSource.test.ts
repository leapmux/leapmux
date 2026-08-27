import { readFileSync } from 'node:fs'
import { dirname, join, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { stripCommentLines } from '~/test-support/sourceScan'
import { collectFiles } from '~/test-support/sourceTree'

// Routing guard: the "this hub still needs a first administrator" rule has
// exactly ONE spelling, `SetupGate`, mounted ONCE around the router outlet.
//
// It got that way from the opposite arrangement. Three surfaces carried a
// private copy (AuthGuard, LoginPage and ElevatePage each called
// isSetupRequired and navigated), and the four addresses that needed it most
// carried none: /signup, /forgot-password, /reset-password and /verify-email
// all served a form that cannot succeed on a hub with no account. Which
// address the rule covered depended on who remembered.
//
// Neither half of the invariant fails loudly on its own. Drop `<SetupGate>`
// from app.tsx and every unit test still passes, because SetupGate.test.tsx
// exercises the component rather than its mounting. Add a private
// isSetupRequired call back to a page and nothing objects either -- until the
// two answers disagree. So this file asserts both halves here, in the fast
// suite, rather than leaves them to the E2E that boots an unseeded hub.

const frontendRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..', '..')
const srcRoot = join(frontendRoot, 'src')

/** The component that owns the rule, and the module that defines the getter. */
const GATE_FILE = 'src/components/common/SetupGate.tsx'
const SYSTEM_INFO_FILE = 'src/lib/systemInfo.ts'

/** Where the gate must be mounted, and the tag that mounts it. */
const ROUTER_ROOT_FILE = 'src/app.tsx'
const MOUNT = '<SetupGate>'

const SOURCE_FILE = /\.tsx?$/
const TEST_FILE = /\.(?:test|spec)\.tsx?$/

/**
 * Every `src/` module that calls `isSetupRequired`, as a repo-relative path.
 *
 * This function blanks comment lines first, so prose that mentions the getter
 * -- which several of these files carry, explaining where the rule went --
 * never counts as a call.
 *
 * Two exemptions, and neither can hide a routing decision. A test file may
 * drive the getter it tests. And `src/test-support/` holds the shared
 * systemInfo mock, which must declare every getter it stands in for; nothing
 * there renders a route.
 */
function callers(): string[] {
  return collectFiles(srcRoot, {
    matches: name => SOURCE_FILE.test(name) && !TEST_FILE.test(name),
    skipPaths: new Set(['test-support']),
  })
    .filter(file => /\bisSetupRequired\b/.test(stripCommentLines(readFileSync(file, 'utf8'))))
    .map(file => relative(frontendRoot, file))
    .sort()
}

describe('setupGate is the single source of the first-run redirect', () => {
  it('is the only caller of isSetupRequired', () => {
    const found = callers()
    expect(
      found,
      'The first-run redirect belongs in SetupGate and nowhere else. A page that '
      + 'decides for itself covers only its own address and drifts from the gate. '
      + `Move the check into ${GATE_FILE}:\n  ${found.join('\n  ')}`,
    ).toEqual([SYSTEM_INFO_FILE, GATE_FILE].sort())
  })

  it('is mounted around the router outlet', () => {
    const source = stripCommentLines(readFileSync(join(frontendRoot, ROUTER_ROOT_FILE), 'utf8'))
    expect(
      source.includes(MOUNT),
      `${ROUTER_ROOT_FILE} must wrap the router outlet in ${MOUNT}. Without it a hub `
      + 'with no account serves every credential page as a form that cannot succeed, '
      + 'and no unit test says a word.',
    ).toBe(true)
  })
})
