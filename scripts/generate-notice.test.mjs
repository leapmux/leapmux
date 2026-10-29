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
  collectRustSpdxTexts,
  compareCrateVersions,
  createOverrideTracker,
  createRustSpdxResolver,
  depHeading,
  headingText,
  listOverrideNames,
  normalizeLicenseText,
  resolveJsLicenseSource,
  RUST_SPDX_LICENSE_SOURCES,
  staleSpdxTextErrors,
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
  it('gives every registered term exactly one origin and a signature', () => {
    for (const [term, source] of Object.entries(RUST_SPDX_LICENSE_SOURCES)) {
      const borrowed = Boolean(source.crate && source.file)
      expect(borrowed !== Boolean(source.vendored), `${term} must be either vendored or crate-backed, not both or neither`).toBe(true)
      expect(source.signature, `${term} has no content signature`).toBeTruthy()
    }
  })

  it('vendors any term whose license body names a copyright holder', () => {
    // Borrowing a crate's file borrows its copyright line. Zlib opens with one,
    // so it comes from scripts/license-overrides/spdx/ rather than from
    // whichever crate happens to be in the graph -- reading the real vendored
    // file here, with no packages at all, since it needs none.
    expect(RUST_SPDX_LICENSE_SOURCES.Zlib.vendored).toBeTruthy()

    const resolved = createRustSpdxResolver([])('Zlib')
    expect(resolved.error, `Zlib: ${resolved.error}`).toBeUndefined()
    expect(resolved.text).toContain('<copyright holders>')
    expect(resolved.text, 'a vendored text must not carry another project\'s copyright').not.toContain('Lokathor')
  })

  it('does not need the graph to resolve a vendored term', () => {
    // The failure this prevents: making Zlib depend on a crate again would
    // reintroduce both the borrowed copyright and an abort when that crate
    // leaves the lock.
    expect(createRustSpdxResolver([])('Zlib').text).toBeTruthy()
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
    return {
      name,
      version,
      manifest_path: join(dir, 'Cargo.toml'),
    }
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
    // rustix is present, but pointed at a directory without the LLVM file.
    const withoutFile = [
      ...packages,
      {
        name: 'rustix',
        version: '1.1.4',
        manifest_path: join(root, 'anyhow-1.0.104', 'Cargo.toml'),
      },
    ]
    const resolved = createRustSpdxResolver(withoutFile)('Apache-2.0 WITH LLVM-exception')
    expect(resolved.error).toContain('is missing')
  })

  it('rejects a file that exists but is not that license', () => {
    // This is the zerocopy regression: LICENSE-BSD existed, so an
    // existence-only check shipped a two-clause text under BSD-3-Clause for
    // years. This fixture's BSD file is missing the third clause.
    const twoClause = [
      ...packages.filter(p => p.name !== 'alloc-no-stdlib'),
      crate('alloc-no-stdlib', '2.0.4', {
        LICENSE: 'Copyright (c) 2016\nRedistribution and use … are permitted provided that the following two conditions are met.',
      }),
    ]
    const resolved = createRustSpdxResolver(twoClause)('BSD-3-Clause')
    expect(resolved.text).toBeUndefined()
    expect(resolved.error).toContain('does not read like BSD-3-Clause')
  })

  it('does not read a term until it is asked for', () => {
    // Laziness is the point of the design: resolving every registered term up
    // front turned a source crate leaving the lock into a hard failure even
    // for a license nothing in the graph declares.
    //
    // Observing that a read did NOT happen needs a seam, so build the resolver
    // while the file is there and remove it before asking. A lazy resolver
    // reads at first ask and reports the loss; an eager one already holds the
    // text and cannot tell the difference. Asserting only that some other term
    // still resolves does not discriminate between the two.
    const doomed = crate('anyhow', '2.0.0', { 'LICENSE-MIT': 'Permission is hereby granted, free of charge …' })
    const resolve = createRustSpdxResolver([doomed])
    rmSync(join(root, 'anyhow-2.0.0', 'LICENSE-MIT'))

    expect(resolve('MIT').text).toBeUndefined()
    expect(resolve('MIT').error).toContain('is missing')
  })

  it('does not let an unresolvable term fail a resolvable one', () => {
    // The pre-fix resolver threw at construction, so one stranded term took
    // the whole run down. MPL-2.0 is unresolvable in this graph; MIT must be
    // unaffected either way.
    const resolve = createRustSpdxResolver(packages)
    expect(() => resolve('MIT')).not.toThrow()
    expect(resolve('MIT').text).toBeTruthy()
  })

  it('reads each term at most once', () => {
    const resolve = createRustSpdxResolver(packages)
    expect(resolve('MIT')).toBe(resolve('MIT'))
  })
})

describe('collectRustSpdxTexts', () => {
  const resolve = (term) => {
    if (term === 'MIT')
      return { text: 'MIT TEXT' }
    if (term === 'Apache-2.0')
      return { text: 'APACHE TEXT' }
    if (term === 'Zlib')
      return { error: 'zlib is broken' }
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
    expect(failures).toEqual([
      { term: 'Zlib', message: 'zlib is broken' },
    ])
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

  it('keeps a parenthesised choice a choice inside a conjunction', () => {
    // The icu4x/zerovec shape. Flattening this to three peers made the whole
    // expression a conjunction, so an unregistered alternative INSIDE the
    // choice rejected a crate whose licensing is satisfied -- the same defect
    // the slash-form case above covers, one nesting level down.
    expect(collectRustSpdxTexts('(MIT OR Unlicense) AND Apache-2.0', resolve).text)
      .toBe('MIT TEXT\n\n-----\n\nAPACHE TEXT')
  })

  it('still requires every operand of a conjunction that holds a choice', () => {
    // The choice half resolving does not excuse the required half: an operand
    // nothing can render leaves NOTICE genuinely incomplete.
    expect(collectRustSpdxTexts('(MIT OR Apache-2.0) AND Unlicense', resolve).text).toBeNull()
  })

  it('rejects a conjunction whose choice half cannot be rendered at all', () => {
    expect(collectRustSpdxTexts('(Zlib OR Unlicense) AND MIT', resolve).text).toBeNull()
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

  it('gives each space its own hyphen rather than collapsing a run', () => {
    // `fnv 1.0.7 (Apache-2.0 / MIT)` is the real case: stripping the slash
    // leaves two adjacent spaces. Collapsing them to one hyphen produced a
    // link GitHub could not resolve, because its slugger replaces each space
    // separately -- so the TOC entry for that crate was dead.
    expect(toAnchor('fnv 1.0.7 (Apache-2.0 / MIT)')).toBe('fnv-107-apache-20--mit')
  })
})

describe('headingText', () => {
  it('reads a plain text node', () => {
    expect(headingText({
      type: 'element',
      children: [
        { type: 'text', value: 'serde 1.0.229' },
      ],
    }))
      .toBe('serde 1.0.229')
  })

  it('concatenates across nested inline nodes', () => {
    // A dependency name carrying markdown punctuation is parsed into nested
    // inline elements. Taking only the first child would slugify a fragment of
    // the name, and the heading's id would stop matching its own TOC link.
    expect(headingText({
      type: 'element',
      children: [
        { type: 'text', value: 'a' },
        {
          type: 'element',
          tagName: 'em',
          children: [
            { type: 'text', value: 'b' },
          ],
        },
        { type: 'text', value: 'c' },
      ],
    })).toBe('abc')
  })

  it('returns an empty string for an element with no children', () => {
    expect(headingText({ type: 'element', tagName: 'h3' })).toBe('')
  })
})

describe('createOverrideTracker', () => {
  const goOverrides = names => ({
    label: 'Go',
    kind: 'go',
    unit: 'runtime module',
    names,
  })

  it('reports an override that no dependency of its name reads', () => {
    // vinxi left the frontend graph while its override stayed behind, and
    // nothing said so: an override is read only for a package that needs it.
    expect(createOverrideTracker().staleErrors(goOverrides(['github.com-gone-module']))).toEqual([
      'Go: scripts/license-overrides/go/github.com-gone-module/ is stale: no runtime module of that name in the dependency graph reads it. Delete it.',
    ])
  })

  it('reports an override whose dependency ships its own license file, with that reason', () => {
    const overrides = createOverrideTracker()
    overrides.redundant('clipboard-win', 'clipboard-win@5.4.1 ships its own license file')

    expect(overrides.staleErrors({
      label: 'Rust',
      kind: 'rust',
      unit: 'crate',
      names: ['clipboard-win'],
    })).toEqual([
      'Rust: scripts/license-overrides/rust/clipboard-win/ is stale: clipboard-win@5.4.1 ships its own license file. Delete it.',
    ])
  })

  it('keeps an override that one version of a dependency still needs, in either order', () => {
    // Two versions of one crate share one override directory. The version that
    // still lacks a license file keeps the override alive.
    const neededFirst = createOverrideTracker()
    neededFirst.needed('clipboard-win')
    neededFirst.redundant('clipboard-win', 'clipboard-win@5.4.1 ships its own license file')
    const redundantFirst = createOverrideTracker()
    redundantFirst.redundant('clipboard-win', 'clipboard-win@5.4.1 ships its own license file')
    redundantFirst.needed('clipboard-win')

    const rust = {
      label: 'Rust',
      kind: 'rust',
      unit: 'crate',
      names: ['clipboard-win'],
    }
    expect(neededFirst.staleErrors(rust)).toEqual([])
    expect(redundantFirst.staleErrors(rust)).toEqual([])
  })

  it('states the first reason when several versions ignore the override', () => {
    const overrides = createOverrideTracker()
    overrides.redundant('a', 'a@1.0.0 ships its own license file')
    overrides.redundant('a', 'a@2.0.0 ships its own license file')

    expect(overrides.staleErrors(goOverrides(['a']))).toEqual([
      'Go: scripts/license-overrides/go/a/ is stale: a@1.0.0 ships its own license file. Delete it.',
    ])
  })

  it('reports nothing when there are no overrides, and only the overrides on disk', () => {
    const overrides = createOverrideTracker()
    overrides.needed('a')
    overrides.redundant('b', 'b@1.0.0 ships its own license file')

    expect(overrides.staleErrors(goOverrides([]))).toEqual([])
    expect(overrides.staleErrors(goOverrides(['a']))).toEqual([])
  })
})

describe('listOverrideNames', () => {
  let root

  beforeAll(() => {
    root = mkdtempSync(join(tmpdir(), 'license-overrides-'))
    for (const dir of [
      'js/@bufbuild/protobuf',
      'js/@xterm/addon-serialize',
      'js/plain-package',
      'go/github.com-a-b',
    ])
      mkdirSync(join(root, dir), { recursive: true })
    mkdirSync(join(root, 'empty'))
    writeFileSync(join(root, 'js/README.md'), 'not an override')
    writeFileSync(join(root, 'js/@xterm/README.md'), 'not an override either')
  })

  afterAll(() => {
    rmSync(root, { recursive: true, force: true })
  })

  it('lists a scoped override as scope/package, beside a plain one, sorted', () => {
    expect(listOverrideNames(join(root, 'js'))).toEqual(['@bufbuild/protobuf', '@xterm/addon-serialize', 'plain-package'])
  })

  it('lists a Go override under its dashed module path', () => {
    expect(listOverrideNames(join(root, 'go'))).toEqual(['github.com-a-b'])
  })

  it('lists nothing for an empty or a missing directory', () => {
    expect(listOverrideNames(join(root, 'empty'))).toEqual([])
    expect(listOverrideNames(join(root, 'absent'))).toEqual([])
  })
})

describe('staleSpdxTextErrors', () => {
  it('accepts every vendored text that the table reads', () => {
    expect(staleSpdxTextErrors(['Zlib.txt'])).toEqual([])
    expect(staleSpdxTextErrors([])).toEqual([])
  })

  it('reports a text that no entry vendors', () => {
    expect(staleSpdxTextErrors(['Zlib.txt', 'GPL-2.0.txt'])).toEqual([
      'Rust: scripts/license-overrides/spdx/GPL-2.0.txt is stale: no RUST_SPDX_LICENSE_SOURCES entry vendors it. Delete it.',
    ])
  })

  it('does not count a borrowed text as vendored', () => {
    // A `{ crate, file }` entry reads from the crate, never from the directory.
    expect(staleSpdxTextErrors(['LICENSE-MIT'], {
      MIT: { crate: 'anyhow', file: 'LICENSE-MIT', signature: 'Permission' },
    })).toEqual([
      'Rust: scripts/license-overrides/spdx/LICENSE-MIT is stale: no RUST_SPDX_LICENSE_SOURCES entry vendors it. Delete it.',
    ])
  })
})

describe('resolveJsLicenseSource', () => {
  const dep = fields => ({
    name: '@scope/pkg',
    version: '2.1.0',
    declared: 'MIT',
    hasLicenseFile: true,
    override: undefined,
    ...fields,
  })

  it('takes the name and the text from a package that declares and ships its license', () => {
    expect(resolveJsLicenseSource(dep({}))).toEqual({ license: 'MIT', text: 'package', override: 'none' })
  })

  it('calls an override redundant when the package declares and ships its license', () => {
    expect(resolveJsLicenseSource(dep({
      override: { license: 'MIT' },
    }))).toEqual({ license: 'MIT', text: 'package', override: 'redundant' })
  })

  it('refuses a license file whose package declares no license, rather than heading it "unknown"', () => {
    expect(resolveJsLicenseSource(dep({ declared: undefined }))).toEqual({
      error: 'JS: @scope/pkg@2.1.0 — package.json declares no license; add scripts/license-overrides/js/@scope/pkg/expected.json with the license that its license file grants',
      override: 'none',
    })
  })

  it('names the override\'s license over the package\'s own text when the package declares none', () => {
    expect(resolveJsLicenseSource(dep({
      declared: undefined,
      override: { license: 'MIT' },
    }))).toEqual({ license: 'MIT', text: 'package', override: 'needed' })
  })

  it('takes the text from the override when the package ships no license file', () => {
    expect(resolveJsLicenseSource(dep({
      hasLicenseFile: false,
      override: { license: 'MIT' },
    }))).toEqual({ license: 'MIT', text: 'override', override: 'needed' })
  })

  it('asks for an override when the package ships no license file and has none', () => {
    expect(resolveJsLicenseSource(dep({ hasLicenseFile: false }))).toEqual({
      error: 'JS: @scope/pkg@2.1.0 — no license file found; add an override in scripts/license-overrides/js/@scope/pkg/',
      override: 'none',
    })
  })

  it('asks for a review when the declared license no longer matches the override, and keeps the override needed', () => {
    expect(resolveJsLicenseSource(dep({
      hasLicenseFile: false,
      declared: 'Apache-2.0',
      override: { license: 'MIT' },
    }))).toEqual({
      error: 'JS: @scope/pkg@2.1.0 — license field changed from "MIT" to "Apache-2.0"; review and update the override in scripts/license-overrides/js/@scope/pkg/',
      override: 'needed',
    })
    expect(resolveJsLicenseSource(dep({
      hasLicenseFile: false,
      declared: undefined,
      override: { license: 'MIT' },
    }))).toEqual({
      error: 'JS: @scope/pkg@2.1.0 — license field changed from "MIT" to "unknown"; review and update the override in scripts/license-overrides/js/@scope/pkg/',
      override: 'needed',
    })
  })
})
