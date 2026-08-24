import { readFileSync } from 'node:fs'
import { dirname, join, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

import * as browserStorage from '~/lib/browserStorage'
import { LOCAL_KEY_SPECS, SESSION_KEY_SPECS } from '~/lib/browserStorage'
import { lineNumberAt, stripCommentLines } from '~/test-support/sourceScan'
import { collectFiles } from '~/test-support/sourceTree'

// Guards the two rules `browserStorage` runs on that its types cannot reach.
//
// A key registered without a scope is already a COMPILE error, because
// `satisfies Record<string, KeySpec>` makes `scope` mandatory. What types cannot
// see is a module that skips the gateway entirely, or a key constant that is
// exported and then never registered.
//
// 1. Nothing calls `localStorage` / `sessionStorage` directly. Every key is
//    scoped to an account and wrapped in a `{v,e}` TTL envelope, and both of
//    those live in the gateway: a raw `setItem` writes an unscoped, unwrapped
//    entry that the next page-load sweep deletes, so it survives exactly one
//    session. It also writes where a second account on the browser can read it,
//    which is the leak the scoping exists to close. This was documented in the
//    module header and in CLAUDE.md and enforced by nothing.
//
// 2. Every exported key constant is registered. `satisfies` checks the shape of
//    the tables; it cannot notice a `KEY_*` that was declared, exported, used by
//    a caller, and left out of them. That key throws on first use — in
//    production, at whatever moment the feature is first touched.
//
// Same shape as `stableContextUsage.test.ts`: a source scan, because what is
// being guarded is a property of the source tree rather than of any runtime.

const frontendRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..', '..')
const srcRoot = join(frontendRoot, 'src')
const gatewayPath = join(srcRoot, 'lib', 'browserStorage.ts')

const SOURCE_FILE = /\.tsx?$/
const TEST_FILE = /\.(?:test|spec)\.tsx?$/

/**
 * Any reference to the two storage globals.
 *
 * The BARE identifier, not a member access. A pattern anchored on the following
 * `.` bans one spelling out of many: `localStorage['k'] = v`,
 * `const s = sessionStorage; s.setItem(...)` and `window['localStorage']` all
 * write exactly the same unscoped, unwrapped entry and all walk past it.
 *
 * The word boundary keeps the gateway's own helpers out: `localStorageGet` and
 * its siblings continue with a word character, so they do not end the match.
 */
const DIRECT_ACCESS = /\b(?:local|session)Storage\b/g

const sourceFiles = collectFiles(srcRoot, {
  matches: name => SOURCE_FILE.test(name) && !TEST_FILE.test(name),
})

describe('browser-storage keys', () => {
  it('scans a source tree that is actually there', () => {
    // A walk that found nothing would make both guards below pass for the
    // wrong reason, quietly retiring them.
    expect(sourceFiles.length).toBeGreaterThan(100)
    expect(sourceFiles).toContain(gatewayPath)
  })

  it('routes every read and write through browserStorage', () => {
    const offenders: string[] = []
    for (const file of sourceFiles) {
      // The gateway is where the one legitimate direct access lives.
      if (file === gatewayPath)
        continue
      // WHOLE comment lines are blanked, rather than deleted, so the reported
      // line number is the one in the original file. Several modules explain
      // their persistence in exactly these words, and prose is not a call.
      //
      // A trailing comment is NOT exempt -- the line still carries code, so
      // `stripCommentLines` keeps it -- and neither is a string literal. Both
      // are reported, and the fix for both is to move the mention onto a
      // comment line of its own. See `sourceScan.ts`.
      const source = stripCommentLines(readFileSync(file, 'utf8'))
      for (const match of source.matchAll(DIRECT_ACCESS))
        offenders.push(`${relative(frontendRoot, file)}:${lineNumberAt(source, match.index)}`)
    }

    expect(
      offenders,
      `Route browser storage through \`~/lib/browserStorage\` (localStorageGet/Set/Remove, `
      + `sessionStorageGet/Set/Has/Remove). A direct call skips the account scope and the TTL `
      + `envelope, so the value is readable by another account on this browser and the next `
      + `page-load sweep deletes it:\n  ${offenders.join('\n  ')}`,
    ).toEqual([])
  })

  it('registers every exported key constant in exactly one table', () => {
    const registered = new Set([
      ...Object.keys(LOCAL_KEY_SPECS),
      ...Object.keys(SESSION_KEY_SPECS),
    ])
    const exported = Object.entries(browserStorage)
      .filter(([name, value]) => /^(?:KEY|PREFIX)_/.test(name) && typeof value === 'string')

    // Same reason as the walk check: an empty list would pass vacuously.
    expect(exported.length).toBeGreaterThan(0)

    const unregistered = exported
      .filter(([, value]) => !registered.has(value as string))
      .map(([name, value]) => `${name} (${String(value)})`)

    expect(
      unregistered,
      `These key constants are exported but registered in neither LOCAL_KEY_SPECS nor `
      + `SESSION_KEY_SPECS, so every access to them throws at runtime:\n  ${unregistered.join('\n  ')}`,
    ).toEqual([])
  })

  it('keeps one key name out of both tables at once', () => {
    // A name in both would resolve to whichever store the caller happened to
    // use, with two different TTLs and no way to tell which value is live.
    const both = Object.keys(LOCAL_KEY_SPECS).filter(name => name in SESSION_KEY_SPECS)

    expect(
      both,
      `These names are registered for BOTH localStorage and sessionStorage:\n  ${both.join('\n  ')}`,
    ).toEqual([])
  })
})
