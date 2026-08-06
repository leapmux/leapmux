// Tests for generate-notice.mjs, run by `bun test` via `task test-scripts`.
//
// NOTICE.md is a legal artifact: the failure mode that matters is not a crash
// but reproducing the WRONG license text for a crate, which nothing downstream
// checks. These pin the pure decisions behind that — which SPDX terms a license
// expression needs, which crate directory a term's text comes from, and whether
// the text found there is actually that license.

import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterAll, beforeAll, describe, expect, it } from 'bun:test'

import {
  RUST_SPDX_LICENSE_SOURCES,
  collectRustSpdxTexts,
  compareCrateVersions,
  createRustSpdxResolver,
  depHeading,
  normalizeLicenseText,
  toAnchor,
} from './generate-notice.mjs'

describe('compareCrateVersions', () => {
  it('orders by numeric component, not lexically', () => {
    expect(compareCrateVersions('0.10.0', '0.2.0')).toBeGreaterThan(0)
    expect(compareCrateVersions('1.0.99', '1.0.104')).toBeLessThan(0)
  })

  it('ranks a release above its own pre-release', () => {
    // The localeCompare(…, {numeric:true}) this replaced got this backwards,
    // so "pick the highest" could resolve a license out of an -alpha dir.
    expect(compareCrateVersions('1.0.0', '1.0.0-alpha')).toBeGreaterThan(0)
    expect(compareCrateVersions('1.0.0-rc.1', '1.0.0')).toBeLessThan(0)
  })

  it('ignores build metadata, which carries no precedence', () => {
    expect(compareCrateVersions('0.6.0+11769913', '0.6.0')).toBe(0)
    expect(compareCrateVersions('0.6.1+abc', '0.6.0+zzz')).toBeGreaterThan(0)
  })

  it('orders two pre-releases of the same version', () => {
    expect(compareCrateVersions('1.0.0-alpha', '1.0.0-beta')).toBeLessThan(0)
  })

  it('treats a missing component as zero', () => {
    expect(compareCrateVersions('1.2', '1.2.0')).toBe(0)
    expect(compareCrateVersions('1.2.1', '1.2')).toBeGreaterThan(0)
  })

  it('reports equality for identical versions', () => {
    expect(compareCrateVersions('1.2.3', '1.2.3')).toBe(0)
  })
})

describe('depHeading', () => {
  it('includes the license when there is one', () => {
    expect(depHeading({ name: 'serde', version: '1.0.229', license: 'MIT OR Apache-2.0' }))
      .toBe('serde 1.0.229 (MIT OR Apache-2.0)')
  })

  it('omits the license for a Go dep, which carries none', () => {
    expect(depHeading({ name: 'golang.org/x/net', version: 'v0.57.0' })).toBe('golang.org/x/net v0.57.0')
  })

  it('omits the version for an extra entry that has none', () => {
    expect(depHeading({ name: 'Inter', license: 'OFL-1.1' })).toBe('Inter (OFL-1.1)')
  })

  it('renders a bare name when it has neither', () => {
    expect(depHeading({ name: 'Something' })).toBe('Something')
  })
})

describe('SPDX license sources', () => {
  it('gives every registered term a crate, file and signature', () => {
    for (const [term, source] of Object.entries(RUST_SPDX_LICENSE_SOURCES)) {
      expect(source.crate, `${term} has no source crate`).toBeTruthy()
      expect(source.file, `${term} has no source file`).toBeTruthy()
      expect(source.signature, `${term} has no content signature`).toBeTruthy()
    }
  })
})

describe('createRustSpdxResolver', () => {
  let root
  let packages

  const crate = (name, version, files) => {
    const dir = join(root, `${name}-${version}`)
    mkdirSync(dir, { recursive: true })
    writeFileSync(join(dir, 'Cargo.toml'), '')
    for (const [file, body] of Object.entries(files)) writeFileSync(join(dir, file), body)
    return { name, version, manifest_path: join(dir, 'Cargo.toml') }
  }

  beforeAll(() => {
    root = mkdtempSync(join(tmpdir(), 'generate-notice-'))
    packages = [
      crate('anyhow', '1.0.104', {
        'LICENSE-MIT': 'Permission is hereby granted, free of charge …',
        'LICENSE-APACHE': '   Apache License\n   Version 2.0',
      }),
      // An older copy, plus a pre-release OF THE SAME core version — the case
      // that decides which of the three wins. (A pre-release of a *higher*
      // version would legitimately outrank 1.0.104; semver only ranks a
      // pre-release below the release it is a pre-release of.)
      crate('anyhow', '1.0.99', { 'LICENSE-MIT': 'STALE — must not be chosen' }),
      crate('anyhow', '1.0.104-rc.1', { 'LICENSE-MIT': 'PRERELEASE — must not be chosen' }),
      crate('alloc-no-stdlib', '2.0.4', { LICENSE: 'Copyright (c) 2016 Dropbox\n… Neither the name of Dropbox …' }),
      // The zerocopy trap: a file that exists but is the WRONG license.
      crate('bytemuck', '1.25.2', { 'LICENSE-ZLIB': 'Copyright (c) 2019\nThis software is provided as-is.' }),
    ]
  })

  afterAll(() => {
    rmSync(root, { recursive: true, force: true })
  })

  it('resolves a term to its crate\'s license text', () => {
    const resolved = createRustSpdxResolver(packages)('MIT')
    expect(resolved.error).toBeUndefined()
    expect(resolved.text).toContain('Permission is hereby granted')
  })

  it('reads the highest release, never a stale or pre-release copy', () => {
    const resolved = createRustSpdxResolver(packages)('MIT')
    expect(resolved.text).not.toContain('STALE')
    expect(resolved.text).not.toContain('PRERELEASE')
  })

  it('reports a term whose source crate left the lock', () => {
    // option-ext is registered for MPL-2.0 but absent from this graph.
    const resolved = createRustSpdxResolver(packages)('MPL-2.0')
    expect(resolved.text).toBeUndefined()
    expect(resolved.error).toContain('no longer in desktop/rust/Cargo.lock')
  })

  it('reports a term whose source crate stopped shipping the file', () => {
    const withoutFile = [...packages, { name: 'rustix', version: '1.1.4', manifest_path: join(root, 'anyhow-1.0.104', 'Cargo.toml') }]
    const resolved = createRustSpdxResolver(withoutFile)('Apache-2.0 WITH LLVM-exception')
    expect(resolved.error).toContain('no longer ships')
  })

  it('rejects a file that exists but is not that license', () => {
    // This is the zerocopy regression: LICENSE-BSD existed, so an
    // existence-only check shipped a two-clause text under BSD-3-Clause for
    // years. The Zlib fixture here is missing its distinctive clause.
    const resolved = createRustSpdxResolver(packages)('Zlib')
    expect(resolved.text).toBeUndefined()
    expect(resolved.error).toContain('does not read like Zlib')
  })

  it('does not resolve a term nothing asked for', () => {
    // Laziness is the point: an absent source crate must not fail the run
    // unless some crate actually needs that term.
    const resolve = createRustSpdxResolver(packages)
    expect(() => resolve('MIT')).not.toThrow()
    // MPL-2.0 is unresolvable in this graph, yet asking for MIT is unaffected.
    expect(resolve('MIT').text).toBeTruthy()
  })

  it('reads each term at most once', () => {
    const resolve = createRustSpdxResolver(packages)
    expect(resolve('MIT')).toBe(resolve('MIT'))
  })
})

describe('collectRustSpdxTexts', () => {
  const resolve = (term) => {
    if (term === 'MIT') return { text: 'MIT TEXT' }
    if (term === 'Apache-2.0') return { text: 'APACHE TEXT' }
    if (term === 'Zlib') return { error: 'zlib is broken' }
    return { error: `unknown ${term}` }
  }

  it('returns nothing for a crate with no declared license', () => {
    expect(collectRustSpdxTexts(null, resolve)).toEqual({ text: null, failures: [] })
  })

  it('resolves a single term', () => {
    expect(collectRustSpdxTexts('MIT', resolve).text).toBe('MIT TEXT')
  })

  it('joins every term of a conjunction', () => {
    const { text } = collectRustSpdxTexts('MIT AND Apache-2.0', resolve)
    expect(text).toContain('MIT TEXT')
    expect(text).toContain('APACHE TEXT')
  })

  it('accepts a choice when one term resolves', () => {
    // "MIT OR Unlicense" — Unlicense is unregistered, but a choice only needs
    // one satisfied term.
    expect(collectRustSpdxTexts('MIT OR Unlicense', resolve).text).toBe('MIT TEXT')
  })

  it('treats the slash form as a choice, exactly like OR', () => {
    // 29 crates in the graph write "MIT/Apache-2.0". Classifying the slash
    // form as a conjunction rejected such a crate outright whenever one of its
    // terms was unregistered, even though the other resolved.
    expect(collectRustSpdxTexts('MIT/Unlicense', resolve).text).toBe('MIT TEXT')
    expect(collectRustSpdxTexts('Unlicense/MIT', resolve).text).toBe('MIT TEXT')
  })

  it('rejects a conjunction with an unregistered term', () => {
    expect(collectRustSpdxTexts('MIT AND Unlicense', resolve).text).toBeNull()
  })

  it('rejects a single unregistered term', () => {
    expect(collectRustSpdxTexts('Unlicense', resolve).text).toBeNull()
  })

  it('surfaces a resolver failure with the term that caused it', () => {
    const { text, failures } = collectRustSpdxTexts('Zlib', resolve)
    expect(text).toBeNull()
    expect(failures).toEqual([{ term: 'Zlib', message: 'zlib is broken' }])
  })

  it('does not let a broken term satisfy a choice on its own', () => {
    expect(collectRustSpdxTexts('Zlib OR Unlicense', resolve).text).toBeNull()
  })

  it('still satisfies a choice whose other term resolves', () => {
    expect(collectRustSpdxTexts('Zlib OR MIT', resolve).text).toBe('MIT TEXT')
  })

  it('strips parentheses and deduplicates repeated terms', () => {
    expect(collectRustSpdxTexts('(MIT OR MIT)', resolve).text).toBe('MIT TEXT')
  })
})

describe('normalizeLicenseText', () => {
  it('strips carriage returns and surrounding blank lines', () => {
    expect(normalizeLicenseText('\r\n\r\nMIT\r\nText\r\n\r\n')).toBe('MIT\nText')
  })

  it('removes triple backticks, which would break the fenced block', () => {
    expect(normalizeLicenseText('a\n```\nb')).toBe('a\n\nb')
  })

  it('returns an empty string for all-blank input', () => {
    expect(normalizeLicenseText('\n\n   \n')).toBe('')
  })
})

describe('toAnchor', () => {
  it('lowercases and hyphenates', () => {
    expect(toAnchor('serde 1.0.229')).toBe('serde-10229')
  })

  it('drops characters a markdown anchor cannot carry', () => {
    expect(toAnchor('golang.org/x/net v0.57.0 (MIT)')).toBe('golangorgxnet-v0570-mit')
  })
})
