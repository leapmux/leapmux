import type { LocalKeySpec } from '~/lib/browserStorage'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'

import { describe, expect, it } from 'vitest'
import * as browserStorage from '~/lib/browserStorage'
import { LOCAL_KEY_SPECS, SESSION_KEY_SPECS } from '~/lib/browserStorage'
import { lineNumberAt, stripCommentLines } from '~/test-support/sourceScan'
import { collectFiles, frontendRoot, posixRelative } from '~/test-support/sourceTree'

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

const srcRoot = join(frontendRoot, 'src')
const gatewayPath = join(srcRoot, 'lib', 'browserStorage.ts')
const storageDbPath = join(srcRoot, 'lib', 'browserStorageDb.ts')
const idbPath = join(srcRoot, 'lib', 'idb.ts')

/**
 * The three modules that may name a storage primitive.
 *
 * `browserStorage` composes the account key and the expiration; `browserStorageDb`
 * is the IndexedDB mechanism behind it; `idb` is the scaffold that opens a
 * database at all. Everything else goes through one of them.
 */
const STORAGE_MODULES = new Set([
  gatewayPath,
  storageDbPath,
  idbPath,
  // Installs a fake IndexedDB for the files that test the asynchronous storage
  // tier. It is test support, not production, and stubbing the global is the
  // whole of what it does.
  join(srcRoot, 'test-support', 'persistentStorage.ts'),
])

const SOURCE_FILE = /\.tsx?$/
const TEST_FILE = /\.(?:test|spec)\.tsx?$/

/**
 * Any reference to a storage global: the two Web Storage ones and the IndexedDB
 * factory.
 *
 * Dexie itself is NOT matched here. A store legitimately names `Dexie` to type
 * the tables its connection hands back, and telling a type reference apart from
 * a construction is a job for the type system; `no-restricted-imports` in
 * `eslint.config.ts` does it properly, with `allowTypeImports`.
 *
 * The BARE identifier, not a member access. A pattern anchored on the following
 * `.` bans one spelling out of many: `localStorage['k'] = v`,
 * `const s = sessionStorage; s.setItem(...)` and `window['localStorage']` all
 * write exactly the same unscoped, unwrapped entry and all walk past it.
 *
 * The word boundary keeps the gateway's own helpers out: `localStorageGet` and
 * its siblings continue with a word character, so they do not end the match.
 */
const DIRECT_ACCESS = /\b(?:local|session)Storage\b|\bindexedDB\b/g

const sourceFiles = collectFiles(srcRoot, {
  matches: name => SOURCE_FILE.test(name) && !TEST_FILE.test(name),
})

describe('browser-storage keys', () => {
  it('scans a source tree that is actually there', () => {
    // A walk that found nothing would make both guards below pass for the
    // wrong reason, quietly retiring them.
    expect(sourceFiles.length).toBeGreaterThan(100)
    for (const module of STORAGE_MODULES)
      expect(sourceFiles).toContain(module)
  })

  it('routes every read and write through browserStorage', () => {
    const offenders: string[] = []
    for (const file of sourceFiles) {
      // The storage modules are where the legitimate direct access lives.
      if (STORAGE_MODULES.has(file))
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
        offenders.push(`${posixRelative(frontendRoot, file)}:${lineNumberAt(source, match.index)}`)
    }

    expect(
      offenders,
      `Route browser storage through \`~/lib/browserStorage\` (localStorageGet/Set/Remove, `
      + `localStorageLoad/Store/Drop, sessionStorageGet/Set/Has/Remove), and IndexedDB through `
      + `\`~/lib/idb\` (createIdbConnection). A direct storage call skips the account scope and `
      + `the expiration, so the value is readable by another account on this browser and the next `
      + `sweep deletes it; a direct \`indexedDB.open\` skips the schema check that rebuilds a `
      + `drifted database:\n  ${offenders.join('\n  ')}`,
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

  // The single-importer rule that keeps `browserStorage` the one module a
  // caller names. `browserStorageDb` is its mechanism, not a second gateway:
  // it knows nothing about key names, scopes, tiers or expiry policy, and a
  // caller that reached it directly would be writing rows the registry never
  // saw and the sweep therefore deletes.
  it('lets nothing but the gateway import the storage mechanism', () => {
    const offenders: string[] = []
    for (const file of sourceFiles) {
      if (file === gatewayPath || file === storageDbPath)
        continue
      const source = stripCommentLines(readFileSync(file, 'utf8'))
      if (source.includes('browserStorageDb'))
        offenders.push(posixRelative(frontendRoot, file))
    }

    expect(
      offenders,
      `Only \`~/lib/browserStorage\` may import \`~/lib/browserStorageDb\`. It is the mechanism `
      + `behind the gateway, not a second gateway: it applies no account scope, no expiration and `
      + `no tier, so a row written through it is one the sweep deletes:\n  ${offenders.join('\n  ')}`,
    ).toEqual([])
  })

  // No type can state either half of this. The relay marks are the only keys
  // that a second account must SHARE rather than be partitioned from, and the
  // only ones whose value is a high-water mark rather than a last write.
  it('keeps the two relay marks device-scoped, synchronous and monotonic', () => {
    const device = (Object.entries(LOCAL_KEY_SPECS) as Array<[string, LocalKeySpec]>)
      .filter(([, spec]) => spec.scope === 'device')
    expect(device.map(([name]) => name).sort())
      .toEqual(['channel-relay-seq', 'user-events-relay-seq'])
    for (const [name, spec] of device) {
      // Synchronous: the allocator is a plain `() => number` called from a
      // channel-wrapper constructor, which has nothing to await on.
      expect(spec.access, name).toBe('sync')
      // Monotonic: a smaller mark overwriting a larger one seeds the next
      // reload below the owner the still-live sidecar holds.
      expect(spec.monotonic, name).toBe(true)
    }
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
