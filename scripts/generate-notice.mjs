#!/usr/bin/env bun
// generate-notice.mjs — Generate NOTICE.md and NOTICE.html with third-party
// license texts.
//
// Usage: bun scripts/generate-notice.mjs
//
// Collects licenses from:
//   - Go modules (via go.work workspace: backend + desktop/go)
//   - Rust crates (desktop/rust/Cargo.lock)
//   - JavaScript runtime dependencies (frontend/node_modules)
//   - Manually vendored assets/code listed under scripts/license-overrides/extra/

import { execSync } from 'node:child_process'
import { existsSync, readdirSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import process from 'node:process'

const ROOT = resolve(import.meta.dirname, '..')
const FRONTEND = join(ROOT, 'frontend')
const DESKTOP_RUST = join(ROOT, 'desktop/rust')
const LICENSE_OVERRIDES_GO = join(ROOT, 'scripts/license-overrides/go')
const LICENSE_OVERRIDES_RUST = join(ROOT, 'scripts/license-overrides/rust')
const LICENSE_OVERRIDES_JS = join(ROOT, 'scripts/license-overrides/js')
const LICENSE_EXTRAS = join(ROOT, 'scripts/license-overrides/extra')
const LICENSE_OVERRIDES_SPDX = join(ROOT, 'scripts/license-overrides/spdx')
// Canonical license texts for crates that declare an SPDX term but ship no
// license file of their own.
//
// Two shapes, and which one an entry takes is a question about the license's
// COPYRIGHT LINE, not about convenience:
//
//   { vendored }      read scripts/license-overrides/spdx/<file>. Use this
//                     when the license body embeds a copyright holder, because
//                     borrowing a crate's file also borrows its holder. Zlib
//                     opens with one, and reproducing bytemuck's
//                     `Copyright (c) 2019 Daniel "Lokathor" Gee.` under
//                     dispatch2, tinyvec and fourteen objc2-* crates credited
//                     all of them to an unrelated project. The vendored text
//                     keeps SPDX's own `<year> <copyright holders>` placeholder
//                     instead of naming the wrong party.
//
//   { crate, file }   borrow the text from a crate in the graph, resolved from
//                     `cargo metadata` at run time so a `cargo update` moves it
//                     with the lock instead of stranding a version-stamped
//                     path. Fine for MIT, Apache-2.0 and MPL-2.0, whose bodies
//                     carry no holder at all -- anyhow's LICENSE-MIT opens at
//                     "Permission is hereby granted". BSD-3-Clause borrows from
//                     alloc-no-stdlib, whose holder IS defensible for its only
//                     consumer: alloc-stdlib, its sibling under the same
//                     Dropbox grant.
//
// `signature` is a phrase the resolved text must contain, either way. Existence
// alone is not enough: the BSD-3-Clause entry pointed at zerocopy's LICENSE-BSD
// for years, which is a *two*-clause text, so every crate that borrowed it
// reproduced the wrong license and only a human reading NOTICE caught on.
export const RUST_SPDX_LICENSE_SOURCES = {
  'Apache-2.0': { crate: 'anyhow', file: 'LICENSE-APACHE', signature: 'Apache License' },
  'Apache-2.0 WITH LLVM-exception': { crate: 'rustix', file: 'LICENSE-Apache-2.0_WITH_LLVM-exception', signature: 'LLVM Exception' },
  'BSD-3-Clause': { crate: 'alloc-no-stdlib', file: 'LICENSE', signature: 'Neither the name' },
  'MIT': { crate: 'anyhow', file: 'LICENSE-MIT', signature: 'Permission is hereby granted' },
  'MPL-2.0': { crate: 'option-ext', file: 'LICENSE.txt', signature: 'Mozilla Public License' },
  'Zlib': { vendored: 'Zlib.txt', signature: 'Altered source versions must be plainly marked' },
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Names (case-insensitive) that indicate a license file. */
const LICENSE_NAMES_RE = /^(?:licen[cs]e|copying|notice|unlicense)(?:[-_. ].+)?$/i

/**
 * Find license files in `dir`, optionally walking up parent directories
 * until `stopAt` (exclusive). Returns the paths from the first directory
 * containing at least one license file.
 */
function findLicenseFiles(dir, stopAt) {
  let current = dir
  while (current.startsWith(stopAt)) {
    try {
      const matches = []
      for (const entry of readdirSync(current, { withFileTypes: true })) {
        if (LICENSE_NAMES_RE.test(entry.name) && entry.isFile()) {
          matches.push(join(current, entry.name))
        }
      }
      if (matches.length > 0)
        return matches.sort()
    }
    catch { /* directory may not exist or be readable */ }
    const parent = dirname(current)
    if (parent === current)
      break
    current = parent
  }
  return []
}

/**
 * Find a single license file in `dir`, optionally walking up parent directories
 * until `stopAt` (exclusive). Returns the file path or null.
 */
function findLicenseFile(dir, stopAt) {
  return findLicenseFiles(dir, stopAt)[0] ?? null
}

/** Normalize license text: strip \r, trim blank lines, remove triple+ backticks. */
export function normalizeLicenseText(text) {
  const lines = text.replace(/\r/g, '').split('\n')
  let start = 0
  while (start < lines.length && lines[start].trim() === '') start++
  let end = lines.length - 1
  while (end >= start && lines[end].trim() === '') end--
  return lines.slice(start, end + 1)
    .map(line => line.replace(/`{3,}/g, ''))
    .join('\n')
}

/**
 * Format a dependency heading: "name version (license)", degrading for the
 * fields an ecosystem does not carry -- Go deps have no `license`, and a
 * hand-listed extra may have no `version`.
 *
 * One formatter for every ecosystem on purpose: the TOC link and the section
 * heading are built from this same string, so `toAnchor` can never be handed a
 * heading the body renders differently.
 */
export function depHeading(dep) {
  const base = dep.version
    ? `${dep.name} ${dep.version}`
    : dep.name
  return dep.license
    ? `${base} (${dep.license})`
    : base
}

/**
 * Order two crate versions by semver precedence.
 *
 * Not `localeCompare(…, {numeric: true})`, which is collation, not semver: it
 * ranks "1.0.0-alpha" ABOVE "1.0.0" because the shorter string is a prefix,
 * and mis-orders "+build" metadata the same way. Picking "the highest" with it
 * would silently read a license out of a pre-release directory.
 *
 * @returns negative, 0, or positive, like a sort comparator
 */
export function compareCrateVersions(a, b) {
  const split = (version) => {
    const dash = version.indexOf('-')
    const core = dash === -1
      ? version
      : version.slice(0, dash)
    const prerelease = dash === -1
      ? ''
      : version.slice(dash + 1)
    // Build metadata ("0.6.0+11769913") carries no precedence; drop it.
    const plus = core.indexOf('+')
    const numbers = (plus === -1
      ? core
      : core.slice(0, plus)).split('.').map(Number)
    return { numbers, prerelease }
  }

  const left = split(a)
  const right = split(b)
  for (let i = 0; i < Math.max(left.numbers.length, right.numbers.length); i++) {
    const delta = (left.numbers[i] ?? 0) - (right.numbers[i] ?? 0)
    if (delta !== 0)
      return delta
  }
  // Same core version: a release outranks any pre-release of it.
  if (left.prerelease === right.prerelease)
    return 0
  if (left.prerelease === '')
    return 1
  if (right.prerelease === '')
    return -1
  return left.prerelease < right.prerelease
    ? -1
    : 1
}

/**
 * Build a `term -> {text} | {error}` resolver over the crates that
 * RUST_SPDX_LICENSE_SOURCES names, reading each file at most once.
 *
 * Lazy on purpose. Resolving every term up front turns a source crate leaving
 * the lock into a hard failure even for a term nothing in the graph declares:
 * `Apache-2.0 WITH LLVM-exception` currently has zero consumers (rustix and
 * its neighbours all ship their own texts), so an eager pass would abort the
 * whole run over a license NOTICE was never going to print. Resolving on
 * demand makes the run fail exactly when NOTICE would otherwise be incomplete.
 *
 * @param {Array<{name: string, version: string, manifest_path: string}>} packages
 */
export function createRustSpdxResolver(packages) {
  const sourceCrates = new Set(
    Object.values(RUST_SPDX_LICENSE_SOURCES).filter(source => source.crate).map(source => source.crate),
  )

  /** @type {Map<string, {version: string, dir: string}>} */
  const crateDirs = new Map()
  for (const pkg of packages) {
    if (!sourceCrates.has(pkg.name))
      continue
    // A crate can appear at more than one version; take the highest so the
    // chosen text never depends on the order `cargo metadata` emits.
    const previous = crateDirs.get(pkg.name)
    if (previous && compareCrateVersions(previous.version, pkg.version) >= 0)
      continue
    crateDirs.set(pkg.name, {
      version: pkg.version,
      dir: dirname(pkg.manifest_path),
    })
  }

  const cache = new Map()
  return function resolveSpdxText(term) {
    if (!cache.has(term))
      cache.set(term, loadSpdxText(term, crateDirs))
    return cache.get(term)
  }
}

/** Read and validate one SPDX term's canonical text. @returns {{text: string} | {error: string}} */
export function loadSpdxText(term, crateDirs) {
  const { crate, file, vendored, signature } = RUST_SPDX_LICENSE_SOURCES[term]

  let path
  let origin
  if (vendored) {
    path = join(LICENSE_OVERRIDES_SPDX, vendored)
    origin = `scripts/license-overrides/spdx/${vendored}`
  }
  else {
    const source = crateDirs.get(crate)
    if (!source) {
      return {
        error: `canonical "${term}" text: crate ${crate} is no longer in desktop/rust/Cargo.lock. `
          + 'Point RUST_SPDX_LICENSE_SOURCES at a crate that is, and that ships this license text.',
      }
    }
    path = join(source.dir, file)
    origin = `${crate} ${source.version} ${file}`
  }

  if (!existsSync(path)) {
    return { error: `canonical "${term}" text: ${origin} is missing (looked in ${path}).` }
  }

  const text = normalizeLicenseText(readFileSync(path, 'utf-8'))
  if (!text.includes(signature)) {
    return {
      error: `canonical "${term}" text: ${origin} does not read like ${term} `
        + `(expected to find ${JSON.stringify(signature)}). Re-point the entry -- reproducing it would ship the wrong license.`,
    }
  }
  return { text }
}

/**
 * Resolve a crate's SPDX expression to the license text NOTICE should
 * reproduce, or null when the expression cannot be satisfied.
 *
 * Reading the expression happens entirely here, including whether it is a
 * choice (OR) or a conjunction (AND). Deciding that at the call site instead
 * split the authority: this function normalizes the slash form (`MIT/Apache-2.0`,
 * which 29 crates in the graph still use) to OR, while a caller testing the raw
 * string for " OR " classified the same expression as a conjunction -- so a
 * slash-form crate with one unregistered term was reported as having no license
 * at all, even though its other term resolved fine.
 *
 * @param {string | null} licenseExpression
 * @param {(term: string) => {text: string} | {error: string}} resolveSpdxText
 * @returns {{text: string | null, failures: Array<{term: string, message: string}>}} the
 *   license text to reproduce (null when the expression cannot be satisfied),
 *   plus one entry per term whose canonical text could not be read
 */
export function collectRustSpdxTexts(licenseExpression, resolveSpdxText) {
  if (!licenseExpression)
    return { text: null, failures: [] }

  const normalized = licenseExpression.replace(/\//g, ' OR ')
  const failures = []
  const texts = []
  const seen = new Set()

  // Split on AND first, so a parenthesised group stays ONE operand. Classifying
  // the whole expression with `includes(' OR ') && !includes(' AND ')` and then
  // splitting on both operators flattened `(MIT OR Apache-2.0) AND Unicode-3.0`
  // into three peers and demanded all three -- so a crate whose choice half
  // resolved fine was still reported as having no license at all, the same
  // failure the slash-form normalization above exists to prevent, one nesting
  // level down.
  //
  // Every operand must be satisfied. Within one, any single term satisfies it,
  // but every term that RESOLVES is still reproduced: a dual-licensed crate
  // grants the reader the choice, so NOTICE shows the alternatives rather than
  // silently electing one on their behalf.
  for (const operand of normalized.split(/\s+AND\s+/)) {
    const terms = [
      ...new Set(
        operand
          .split(/\s+OR\s+/)
          .map(term => term.replace(/[()]/g, '').trim())
          .filter(Boolean),
      ),
    ]

    let resolvedHere = 0
    let unregistered = 0
    for (const term of terms) {
      if (!(term in RUST_SPDX_LICENSE_SOURCES)) {
        unregistered++
        continue
      }
      const resolved = resolveSpdxText(term)
      if (resolved.error) {
        failures.push({ term, message: resolved.error })
        continue
      }
      resolvedHere++
      if (!seen.has(term)) {
        seen.add(term)
        texts.push(resolved.text)
      }
    }

    // A lone term is a requirement, so failing to render it leaves NOTICE
    // genuinely incomplete. Inside a real choice, an unrenderable alternative
    // costs nothing as long as another one rendered.
    const satisfied = terms.length > 1
      ? resolvedHere > 0
      : resolvedHere > 0 && unregistered === 0
    if (!satisfied)
      return { text: null, failures }
  }

  return {
    text: texts.length > 0
      ? texts.join('\n\n-----\n\n')
      : null,
    failures,
  }
}

/**
 * Track which license overrides of one kind supply a license text, so that a
 * collector can report every override that supplies none.
 *
 * Nothing else notices a stale override. A collector reads an override only
 * for a dependency that ships no license file, so an override for a package
 * that left the graph, or for one that started shipping its own file, sits in
 * the tree unread while it still looks maintained.
 *
 * `needed(name)` wins over `redundant(name, reason)` in either order, because a
 * package can appear at several versions and one version that lacks a license
 * file still needs the override.
 */
export function createOverrideTracker() {
  const needed = new Set()
  /** @type {Map<string, string>} */
  const redundant = new Map()
  return {
    /** A dependency of this name takes, or may take, its license text from the override. */
    needed(name) {
      needed.add(name)
    },
    /** A dependency of this name exists but has no use for the override, for `reason`. */
    redundant(name, reason) {
      if (!redundant.has(name))
        redundant.set(name, reason)
    },
    /**
     * One error for each override in `names` that no dependency needed.
     *
     * @param {{label: string, kind: string, unit: string, names: string[]}} overrides
     *   `label` opens the message (`Go`), `kind` is the directory under
     *   scripts/license-overrides/ (`go`), and `unit` says what a dependency is
     *   for this kind (`runtime module`)
     */
    staleErrors({ label, kind, unit, names }) {
      const errors = []
      for (const name of names) {
        if (needed.has(name))
          continue
        const reason = redundant.get(name) ?? `no ${unit} of that name in the dependency graph reads it`
        errors.push(`${label}: scripts/license-overrides/${kind}/${name}/ is stale: ${reason}. Delete it.`)
      }
      return errors
    },
  }
}

/**
 * Decide which license a JS dependency's NOTICE entry names, where its text
 * comes from, and what that makes of the dependency's override.
 *
 * A package that ships a license file but declares no license in package.json
 * would otherwise head its text "(unknown)" -- @solidjs/start 2.1.0 dropped the
 * field it carried through 1.x -- so that case needs an override that states
 * the license the file grants, and fails without one.
 *
 * @param {{name: string, version: string, declared: string | undefined, hasLicenseFile: boolean,
 *   override: {license: string} | undefined}} dep `declared` is package.json's `license`,
 *   and `override` is the dependency's expected.json, when it has one
 * @returns {{license: string, text: 'package' | 'override', override: 'needed' | 'redundant' | 'none'}
 *   | {error: string, override: 'needed' | 'none'}} the license to name and the
 *   directory to read its text from, or the error to report; either way, whether
 *   the dependency needs its override
 */
export function resolveJsLicenseSource({ name, version, declared, hasLicenseFile, override }) {
  const where = `scripts/license-overrides/js/${name}/`
  if (hasLicenseFile) {
    if (declared !== undefined) {
      return {
        license: declared,
        text: 'package',
        override: override ? 'redundant' : 'none',
      }
    }
    if (!override)
      return { error: `JS: ${name}@${version} — package.json declares no license; add ${where}expected.json with the license that its license file grants`, override: 'none' }
    return { license: override.license, text: 'package', override: 'needed' }
  }
  if (!override)
    return { error: `JS: ${name}@${version} — no license file found; add an override in ${where}`, override: 'none' }
  // The override counts as needed even when this check rejects it, because the
  // error already asks for a review, which a stale report would contradict.
  const field = declared ?? 'unknown'
  if (override.license !== field)
    return { error: `JS: ${name}@${version} — license field changed from "${override.license}" to "${field}"; review and update the override in ${where}`, override: 'needed' }
  return { license: field, text: 'override', override: 'needed' }
}

/**
 * List the override names under one kind's directory, sorted. An npm scope
 * directory (`@bufbuild`) holds one override per package of that scope, so
 * its entries list as `@bufbuild/protobuf`. A missing directory lists none.
 */
export function listOverrideNames(dir) {
  if (!existsSync(dir))
    return []
  const names = []
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    if (!entry.isDirectory())
      continue
    if (entry.name.startsWith('@')) {
      for (const scoped of readdirSync(join(dir, entry.name), { withFileTypes: true })) {
        if (scoped.isDirectory())
          names.push(`${entry.name}/${scoped.name}`)
      }
    }
    else {
      names.push(entry.name)
    }
  }
  return names.sort()
}

/**
 * One error for each vendored SPDX text that no RUST_SPDX_LICENSE_SOURCES entry
 * reads. An entry with no consumer in the graph stays valid (see
 * createRustSpdxResolver), but a file that no entry points at can never reach
 * NOTICE.
 *
 * @param {string[]} files the file names in scripts/license-overrides/spdx/
 */
export function staleSpdxTextErrors(files, sources = RUST_SPDX_LICENSE_SOURCES) {
  const vendored = new Set(Object.values(sources).map(source => source.vendored).filter(Boolean))
  return files
    .filter(file => !vendored.has(file))
    .map(file => `Rust: scripts/license-overrides/spdx/${file} is stale: no RUST_SPDX_LICENSE_SOURCES entry vendors it. Delete it.`)
}

/**
 * Slugify a heading for use as a markdown anchor.
 *
 * Each space becomes its own `-`, rather than a run of whitespace collapsing
 * to one. That is what GitHub's slugger does, and a heading like
 * `fnv 1.0.7 (Apache-2.0 / MIT)` -- whose stripped ` / ` leaves two adjacent
 * spaces -- is the case where collapsing produced a link GitHub could not
 * resolve.
 */
export function toAnchor(heading) {
  return heading
    .toLowerCase()
    .replace(/[^a-z0-9 _-]/g, '')
    .replace(/ /g, '-')
}

/**
 * The plain text of a hast element, concatenated across descendants.
 *
 * A heading is normally one text node, but a dependency name carrying markdown
 * punctuation can be parsed into nested inline nodes, and taking only the first
 * child would silently slugify a fragment of the name.
 */
export function headingText(node) {
  if (node.type === 'text')
    return node.value
  return (node.children ?? []).map(headingText).join('')
}

// ---------------------------------------------------------------------------
// Go dependencies
// ---------------------------------------------------------------------------

/**
 * Parse a stream of concatenated top-level JSON objects (the format
 * emitted by `go list -json`) into an array. Brace-balance walk —
 * safe for strings containing `{`/`}` since we stay at depth 0 outside
 * of JSON strings.
 */
function parseGoListJson(raw) {
  const objs = []
  let depth = 0
  let start = -1
  let inStr = false
  let esc = false
  for (let i = 0; i < raw.length; i++) {
    const c = raw[i]
    if (inStr) {
      if (esc)
        esc = false
      else if (c === '\\')
        esc = true
      else if (c === '"')
        inStr = false
      continue
    }
    if (c === '"') {
      inStr = true
      continue
    }
    if (c === '{') {
      if (depth === 0)
        start = i
      depth++
    }
    else if (c === '}') {
      depth--
      if (depth === 0 && start >= 0) {
        objs.push(JSON.parse(raw.slice(start, i + 1)))
        start = -1
      }
    }
  }
  return objs
}

/**
 * Collect the set of `path@version` module keys reachable from
 * non-tool packages across every workspace module, enumerated for
 * each supported build target so platform-specific deps (e.g.
 * go-winio on Windows) are included.
 *
 * Tool-only deps (declared via `tool` directives in go.mod and
 * reachable only from tool main packages) are deliberately excluded —
 * their binaries are built and run separately via `go tool <name>`
 * and are never linked into a shipped LeapMux binary.
 */
function collectGoRuntimeModuleKeys() {
  // Workspace directories listed in go.work `use (…)` block.
  const workText = readFileSync(join(ROOT, 'go.work'), 'utf-8')
  const useMatch = workText.match(/use\s*\(([^)]*)\)/)
  const workDirs = useMatch
    ? useMatch[1].split('\n').map(l => l.trim()).filter(l => l && !l.startsWith('//'))
    : []
  if (workDirs.length === 0) {
    throw new Error('generate-notice: no `use (…)` directories found in go.work')
  }

  // Build targets LeapMux ships for. GOARCH rarely changes the dep
  // graph relative to GOOS, so sticking to amd64 per-OS is enough.
  const targets = [
    { GOOS: 'linux', GOARCH: 'amd64' },
    { GOOS: 'darwin', GOARCH: 'amd64' },
    { GOOS: 'windows', GOARCH: 'amd64' },
  ]

  const keys = new Set()
  for (const dir of workDirs) {
    const cwd = join(ROOT, dir)
    for (const t of targets) {
      console.log(`  listing runtime packages: ${dir} (${t.GOOS}/${t.GOARCH}) …`)
      const raw = execSync('go list -deps -test -json ./...', {
        cwd,
        encoding: 'utf-8',
        env: { ...process.env, GOOS: t.GOOS, GOARCH: t.GOARCH },
        maxBuffer: 128 * 1024 * 1024,
      })
      for (const pkg of parseGoListJson(raw)) {
        const m = pkg.Module
        if (m && !m.Main)
          keys.add(`${m.Path}@${m.Version}`)
      }
    }
  }
  return keys
}

function collectGoDeps() {
  // Ensure all modules are downloaded so Dir fields are populated.
  console.log('Downloading Go modules …')
  execSync('go mod download', { cwd: ROOT, stdio: 'inherit' })

  console.log('Listing Go runtime dependency closure …')
  const runtimeKeys = collectGoRuntimeModuleKeys()

  console.log('Listing Go modules …')
  const raw = execSync('go list -m -json all', { cwd: ROOT, encoding: 'utf-8' })
  const modules = parseGoListJson(raw)

  /** @type {Map<string, {name: string, version: string, licenseText: string}>} */
  const deps = new Map()
  const warnings = []
  const errors = []
  const overrides = createOverrideTracker()

  // Determine Go module cache root for walking up parent dirs.
  const goModCache = execSync('go env GOMODCACHE', { cwd: ROOT, encoding: 'utf-8' }).trim()

  for (const mod of modules) {
    if (mod.Main)
      continue
    const key = `${mod.Path}@${mod.Version}`
    if (deps.has(key))
      continue
    if (!runtimeKeys.has(key))
      continue // skip tool-only deps

    // A manual override in scripts/license-overrides/go/ is named after the
    // module path with slashes replaced by dashes.
    const overrideName = mod.Path.replace(/\//g, '-')
    const overrideDir = join(LICENSE_OVERRIDES_GO, overrideName)
    const hasOverride = existsSync(join(overrideDir, 'expected.json'))

    let dir = mod.Dir
    if (!dir) {
      // Fallback: construct the expected cache path.
      dir = join(goModCache, `${mod.Path}@${mod.Version}`)
      if (!existsSync(dir)) {
        warnings.push(`Go: ${key} — no Dir and cache miss`)
        // Without the module's own files, nothing shows that the override is
        // stale, so the warning above is the whole report.
        overrides.needed(overrideName)
        continue
      }
    }

    let licFile = findLicenseFile(dir, goModCache)
    if (hasOverride) {
      if (licFile) {
        overrides.redundant(overrideName, `${key} ships its own license file`)
      }
      else {
        overrides.needed(overrideName)
        licFile = findLicenseFile(overrideDir, overrideDir)
      }
    }
    if (!licFile) {
      errors.push(`Go: ${key} — no license file found in ${dir}`)
      continue
    }

    deps.set(key, {
      name: mod.Path,
      version: mod.Version,
      licenseText: normalizeLicenseText(readFileSync(licFile, 'utf-8')),
    })
  }

  errors.push(...overrides.staleErrors({
    label: 'Go',
    kind: 'go',
    unit: 'runtime module',
    names: listOverrideNames(LICENSE_OVERRIDES_GO),
  }))

  return {
    deps: [
      ...deps.values(),
    ].sort((a, b) => a.name.localeCompare(b.name)),
    warnings,
    errors,
  }
}

// ---------------------------------------------------------------------------
// Rust dependencies
// ---------------------------------------------------------------------------

function collectRustDeps() {
  console.log('Fetching Rust crates …')
  execSync('cargo fetch --locked', { cwd: DESKTOP_RUST, stdio: 'inherit' })

  console.log('Listing Rust crates …')
  const raw = execSync('cargo metadata --format-version 1 --locked', {
    cwd: DESKTOP_RUST,
    encoding: 'utf-8',
    maxBuffer: 32 * 1024 * 1024,
  })
  const metadata = JSON.parse(raw)
  const resolveSpdxText = createRustSpdxResolver(metadata.packages ?? [])

  /** @type {Map<string, {name: string, version: string, license: string | null, licenseText: string}>} */
  const deps = new Map()
  const errors = []
  // Deduped: one unresolvable term is a table problem, not a per-crate one, so
  // it is reported once no matter how many crates asked for it. The crates
  // themselves are still named individually below.
  const spdxFailures = new Map()
  const overrides = createOverrideTracker()

  for (const pkg of metadata.packages ?? []) {
    if (pkg.id === metadata.resolve?.root || pkg.source == null)
      continue

    const key = `${pkg.name}@${pkg.version}`
    if (deps.has(key))
      continue

    const manifestDir = dirname(pkg.manifest_path)

    // One walk of the manifest directory, reused: findLicenseFile is just
    // findLicenseFiles(...)[0], so asking for both re-scanned the same
    // directory for ~490 of the graph's crates.
    const manifestLicenses = findLicenseFiles(manifestDir, manifestDir)

    // `license_file` names a file Cargo.toml points at explicitly; no crate in
    // the current graph sets it, but honour it ahead of discovery when one does.
    const declared = pkg.license_file
      ? join(manifestDir, pkg.license_file)
      : null

    const overrideDir = join(LICENSE_OVERRIDES_RUST, pkg.name)
    const hasOverride = existsSync(join(overrideDir, 'expected.json'))

    /** @type {string[]} */
    let licenseFiles = declared && existsSync(declared)
      ? [declared]
      : manifestLicenses
    if (licenseFiles.length > 0) {
      if (hasOverride)
        overrides.redundant(pkg.name, `${key} ships its own license file`)
    }
    else if (hasOverride) {
      // Nothing on disk: fall back to a hand-curated override directory.
      overrides.needed(pkg.name)
      licenseFiles = findLicenseFiles(overrideDir, overrideDir)
    }

    let licenseText = null
    if (licenseFiles.length > 0) {
      licenseText = licenseFiles.map(path => normalizeLicenseText(readFileSync(path, 'utf-8'))).join('\n\n-----\n\n')
    }
    else {
      const { text, failures } = collectRustSpdxTexts(pkg.license, resolveSpdxText)
      for (const failure of failures) spdxFailures.set(failure.term, failure.message)

      if (text === null) {
        errors.push(failures.length > 0
          ? `Rust: ${key} — needs the ${failures.map(f => `"${f.term}"`).join(' + ')} text, which could not be resolved`
          : `Rust: ${key} — no license file found in ${manifestDir}`)
        continue
      }
      licenseText = text
    }

    deps.set(key, {
      name: pkg.name,
      version: pkg.version,
      license: pkg.license ?? null,
      licenseText,
    })
  }

  // Appended after the per-crate lines so the run reports every license gap in
  // one pass: throwing here instead would abort before the JS and extra
  // collectors ran, and hide the Go errors already gathered.
  errors.push(...spdxFailures.values())
  errors.push(...overrides.staleErrors({
    label: 'Rust',
    kind: 'rust',
    unit: 'crate',
    names: listOverrideNames(LICENSE_OVERRIDES_RUST),
  }))
  const spdxFiles = existsSync(LICENSE_OVERRIDES_SPDX)
    ? readdirSync(LICENSE_OVERRIDES_SPDX, { withFileTypes: true }).filter(entry => entry.isFile()).map(entry => entry.name)
    : []
  errors.push(...staleSpdxTextErrors(spdxFiles))

  return {
    deps: [
      ...deps.values(),
    ].sort((a, b) => a.name.localeCompare(b.name)),
    warnings: [],
    errors,
  }
}

// ---------------------------------------------------------------------------
// JavaScript dependencies
// ---------------------------------------------------------------------------

function collectJsDeps() {
  const pkgJsonPath = join(FRONTEND, 'package.json')
  const pkgJson = JSON.parse(readFileSync(pkgJsonPath, 'utf-8'))
  const runtimeDeps = new Set(Object.keys(pkgJson.dependencies ?? {}))

  /** @type {Array<{name: string, version: string, licenseText: string}>} */
  const deps = []
  const warnings = []
  const errors = []
  const overrides = createOverrideTracker()

  const nodeModules = join(FRONTEND, 'node_modules')
  if (!existsSync(nodeModules)) {
    warnings.push('JS: node_modules not found — run `bun install` first')
    return { deps, warnings, errors }
  }

  // Collect package directories (flat + scoped).
  /** @type {Array<{pkgDir: string, pkgName: string}>} */
  const packages = []

  for (const entry of readdirSync(nodeModules)) {
    if (entry.startsWith('.'))
      continue
    const full = join(nodeModules, entry)
    if (entry.startsWith('@')) {
      // Scoped package — enumerate children.
      try {
        for (const child of readdirSync(full)) {
          if (child.startsWith('.'))
            continue
          packages.push({
            pkgDir: join(full, child),
            pkgName: `${entry}/${child}`,
          })
        }
      }
      catch { /* ignore */ }
    }
    else {
      packages.push({ pkgDir: full, pkgName: entry })
    }
  }

  for (const { pkgDir, pkgName } of packages) {
    if (!runtimeDeps.has(pkgName))
      continue

    let meta
    try {
      meta = JSON.parse(readFileSync(join(pkgDir, 'package.json'), 'utf-8'))
    }
    catch {
      continue
    }

    const version = meta.version ?? 'unknown'
    const packageLicenseFile = findLicenseFile(pkgDir, nodeModules)
    const overrideDir = join(LICENSE_OVERRIDES_JS, pkgName)
    const expectedPath = join(overrideDir, 'expected.json')
    const source = resolveJsLicenseSource({
      name: pkgName,
      version,
      declared: meta.license,
      hasLicenseFile: packageLicenseFile !== null,
      override: existsSync(expectedPath)
        ? JSON.parse(readFileSync(expectedPath, 'utf-8'))
        : undefined,
    })

    if (source.override === 'needed')
      overrides.needed(pkgName)
    else if (source.override === 'redundant')
      overrides.redundant(pkgName, `${pkgName}@${version} ships its own license file and declares its license`)
    if (source.error) {
      errors.push(source.error)
      continue
    }

    const licFile = source.text === 'override'
      ? findLicenseFile(overrideDir, overrideDir)
      : packageLicenseFile
    if (!licFile) {
      errors.push(`JS: ${pkgName}@${version} — the override in scripts/license-overrides/js/${pkgName}/ holds no license file`)
      continue
    }

    deps.push({
      name: pkgName,
      version,
      license: source.license,
      licenseText: normalizeLicenseText(readFileSync(licFile, 'utf-8')),
    })
  }

  errors.push(...overrides.staleErrors({
    label: 'JS',
    kind: 'js',
    unit: 'runtime dependency',
    names: listOverrideNames(LICENSE_OVERRIDES_JS),
  }))

  deps.sort((a, b) => a.name.localeCompare(b.name))
  return { deps, warnings, errors }
}

// ---------------------------------------------------------------------------
// Vendored / manually listed dependencies
// ---------------------------------------------------------------------------
//
// Each subdirectory of scripts/license-overrides/extra/ describes one
// third-party item that is not tracked by any package manager — typically
// because its source was copy-pasted or vendored into the repo (e.g. SVG
// path data adapted from an icon pack, or JSON assets fetched via a bespoke
// download step).
//
// Layout:
//   scripts/license-overrides/extra/<slug>/
//     metadata.json   — { "name", "license", "version"?, "url"?, "description"? }
//     LICENSE         — full license text (or any file matching LICENSE_NAMES_RE)
//
// `description` is rendered as a markdown paragraph between the Source link
// and the license text — useful for clarifying what was vendored or how the
// asset is used.

function collectExtraDeps() {
  /** @type {Array<{name: string, version: string | null, license: string | null, url: string | null, description: string | null, licenseText: string}>} */
  const deps = []
  const warnings = []
  const errors = []

  if (!existsSync(LICENSE_EXTRAS)) {
    return { deps, warnings, errors }
  }

  for (const entry of readdirSync(LICENSE_EXTRAS, { withFileTypes: true })) {
    if (!entry.isDirectory() || entry.name.startsWith('.'))
      continue
    const dir = join(LICENSE_EXTRAS, entry.name)
    const metaPath = join(dir, 'metadata.json')

    if (!existsSync(metaPath)) {
      errors.push(`Extra: ${entry.name} — missing metadata.json`)
      continue
    }

    let meta
    try {
      meta = JSON.parse(readFileSync(metaPath, 'utf-8'))
    }
    catch (err) {
      errors.push(`Extra: ${entry.name} — invalid metadata.json: ${err.message}`)
      continue
    }

    if (typeof meta.name !== 'string' || meta.name.length === 0) {
      errors.push(`Extra: ${entry.name} — metadata.json is missing required field "name"`)
      continue
    }
    if (typeof meta.license !== 'string' || meta.license.length === 0) {
      errors.push(`Extra: ${entry.name} — metadata.json is missing required field "license"`)
      continue
    }

    const licFile = findLicenseFile(dir, dir)
    if (!licFile) {
      errors.push(`Extra: ${entry.name} — no license file found in ${dir}`)
      continue
    }

    deps.push({
      name: meta.name,
      version: typeof meta.version === 'string' && meta.version.length > 0
        ? meta.version
        : null,
      license: meta.license,
      url: typeof meta.url === 'string' && meta.url.length > 0
        ? meta.url
        : null,
      description: typeof meta.description === 'string' && meta.description.length > 0
        ? meta.description
        : null,
      licenseText: normalizeLicenseText(readFileSync(licFile, 'utf-8')),
    })
  }

  deps.sort((a, b) => a.name.localeCompare(b.name))
  return { deps, warnings, errors }
}

// ---------------------------------------------------------------------------
// Generate NOTICE.md
// ---------------------------------------------------------------------------

function buildMarkdown(goDeps, rustDeps, jsDeps, extraDeps) {
  const lines = []

  lines.push('# Third-Party Licenses')
  lines.push('')
  lines.push('This file lists the licenses of third-party dependencies used by LeapMux.')
  lines.push('')
  lines.push('For the latest version, see [NOTICE.md on GitHub](https://github.com/leapmux/leapmux/blob/main/NOTICE.md).')
  lines.push('')

  // One list drives both the table of contents and the body, so a section can
  // never be linked without being rendered (or rendered without being linked).
  // `preamble` is what kept the "Other" section from fitting the shape before.
  const sections = [
    { title: 'Go Dependencies', deps: goDeps },
    { title: 'Rust Dependencies', deps: rustDeps },
    { title: 'JavaScript Dependencies', deps: jsDeps },
    {
      title: 'Other Third-Party Notices',
      deps: extraDeps,
      preamble: [
        'The following items are not tracked by any package manager — their',
        'sources were copied or adapted into this repository. They are listed',
        'separately to make their provenance explicit.',
      ],
    },
  ].filter(section => section.deps.length > 0)

  // Table of contents
  lines.push('## Table of Contents')
  lines.push('')
  for (const { title, deps } of sections) {
    lines.push(`### ${title}`)
    lines.push('')
    for (const dep of deps) {
      const heading = depHeading(dep)
      lines.push(`- [${heading}](#${toAnchor(heading)})`)
    }
    lines.push('')
  }

  for (const { title, deps, preamble } of sections) {
    lines.push('---')
    lines.push('')
    lines.push(`## ${title}`)
    lines.push('')
    if (preamble) {
      lines.push(...preamble)
      lines.push('')
    }
    for (const dep of deps) {
      lines.push(`### ${depHeading(dep)}`)
      lines.push('')
      // Only the hand-listed entries carry provenance; the package managers
      // supply neither field, so this is inert for the other three sections.
      if (dep.url) {
        lines.push(`Source: <${dep.url}>`)
        lines.push('')
      }
      if (dep.description) {
        lines.push(dep.description)
        lines.push('')
      }
      lines.push('```')
      lines.push(dep.licenseText)
      lines.push('```')
      lines.push('')
    }
  }

  return lines.join('\n')
}

// ---------------------------------------------------------------------------
// Generate NOTICE.html — standalone page with Oat CSS + LeapMux themes
// ---------------------------------------------------------------------------

async function buildHtml(markdown) {
  // Use remark/rehype from frontend/node_modules to render markdown to HTML.
  const { unified } = await import(join(FRONTEND, 'node_modules/unified/index.js'))
  const { default: remarkParse } = await import(join(FRONTEND, 'node_modules/remark-parse/index.js'))
  const { default: remarkGfm } = await import(join(FRONTEND, 'node_modules/remark-gfm/index.js'))
  const { default: remarkRehype } = await import(join(FRONTEND, 'node_modules/remark-rehype/index.js'))
  const { default: rehypeStringify } = await import(join(FRONTEND, 'node_modules/rehype-stringify/index.js'))

  // Give every heading the id its own table-of-contents link points at.
  // Without this the HTML carried ~700 `href="#..."` links and not one `id`,
  // so every entry in the contents of the in-product licence page was dead.
  // NOTICE.md looks fine on GitHub only because GitHub injects the ids itself.
  //
  // Deliberately toAnchor rather than rehype-slug: the link half of the pair
  // is already generated from toAnchor, so deriving the id from anything else
  // would be a second slug authority that could disagree with it -- which is
  // exactly the bug, in a subtler form.
  const rehypeHeadingIds = () => (tree) => {
    for (const node of tree.children ?? []) {
      if (node.type !== 'element' || !/^h[1-6]$/.test(node.tagName))
        continue
      node.properties ??= {}
      node.properties.id = toAnchor(headingText(node))
    }
  }

  const bodyHtml = String(
    await unified()
      .use(remarkParse)
      .use(remarkGfm)
      .use(remarkRehype)
      .use(rehypeHeadingIds)
      .use(rehypeStringify)
      .process(markdown),
  )

  // Read Oat CSS to inline.
  const oatCss = readFileSync(join(FRONTEND, 'node_modules/@knadh/oat/oat.min.css'), 'utf-8')

  // LeapMux theme overrides, taken FROM the app's palette rather than retyped.
  // These used to be 34 hand-copied literals under a comment claiming they were
  // "extracted from global.css.ts" -- 33 still matched and one had already
  // drifted, which is what a second copy of a palette does over time.
  // The modules under src/styles/themes/ are plain data with no imports outside
  // that directory precisely so this script can read them under bun, where
  // there is no Vite to compile a .css.ts module.
  //
  // The DEFAULT theme only. This page has no preference store, so it cannot
  // know which of the app's themes a reader chose; it follows the OS through a
  // media query, the way it did when the app had one palette.
  const { defaultTheme } = await import(join(FRONTEND, 'src/styles/themes/default.ts'))
  // The default theme's default variant per polarity. This page has no
  // preference store, so it can show one light and one dark look and no more.
  const variantFor = (polarity) => {
    const id = defaultTheme.defaults[polarity]
    const variant = defaultTheme.variants.find(v => v.id === id)
    // Named, because the failure lands HERE and the fault is in the theme
    // module. An unguarded `.find(...).palette` failed the whole build with
    // `Cannot read properties of undefined (reading 'palette')` pointing at a
    // line in a notice generator, which says nothing about which theme file
    // carries a `defaults` id that no variant of its own answers.
    if (!variant)
      throw new Error(`theme "${defaultTheme.id}" names no ${polarity} variant "${id}"`)
    return variant.palette
  }
  const lightPalette = variantFor('light')
  const darkPalette = variantFor('dark')
  const declarations = (palette, indent) =>
    Object.entries(palette).map(([name, value]) => `${indent}${name}: ${value};`).join('\n')

  // Fonts and color-scheme are this page's own. The app resolves its fonts
  // through `var(--ui-font-family, ...)`, and a standalone HTML file has no
  // preference store behind that indirection; it also follows the OS via a
  // media query rather than the app's [data-theme] attribute.
  const themeCss = `
/* LeapMux light theme */
:root {
${declarations(lightPalette, '  ')}
  --font-sans: system-ui, sans-serif;
  --font-mono: "SF Mono", Consolas, monospace;
}

/* LeapMux dark theme */
@media (prefers-color-scheme: dark) {
  :root {
${declarations(darkPalette, '    ')}
    color-scheme: dark;
  }
}

code, pre {
  background-color: rgb(from var(--foreground) r g b / 0.075);
}
pre code, pre pre, code pre, code code {
  background-color: transparent;
}
body {
  max-width: 900px;
  margin: 0 auto;
  padding: var(--space-6);
}
`

  return `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Third-Party Licenses — LeapMux</title>
<style>${oatCss}</style>
<style>${themeCss}</style>
</head>
<body>
${bodyHtml}
</body>
</html>
`
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

async function generateNotice() {
  const go = collectGoDeps()
  const rust = collectRustDeps()
  const js = collectJsDeps()
  const extra = collectExtraDeps()
  const allWarnings = [
    ...go.warnings,
    ...rust.warnings,
    ...js.warnings,
    ...extra.warnings,
  ]
  const allErrors = [
    ...go.errors,
    ...rust.errors,
    ...js.errors,
    ...extra.errors,
  ]

  if (allWarnings.length > 0) {
    console.warn('\nWarnings:')
    for (const w of allWarnings) console.warn(`  ⚠ ${w}`)
    console.warn()
  }

  if (allErrors.length > 0) {
    console.error('\nErrors:')
    for (const e of allErrors) console.error(`  ✗ ${e}`)
    console.error()
    process.exit(1)
  }

  const markdown = buildMarkdown(go.deps, rust.deps, js.deps, extra.deps)

  const mdPath = join(ROOT, 'NOTICE.md')
  writeFileSync(mdPath, markdown, 'utf-8')
  console.log(`✓ Written ${mdPath}`)

  console.log('Rendering HTML …')
  const html = await buildHtml(markdown)
  const htmlPath = join(ROOT, 'NOTICE.html')
  writeFileSync(htmlPath, html, 'utf-8')
  console.log(`✓ Written ${htmlPath}`)

  console.log(`  (${go.deps.length} Go + ${rust.deps.length} Rust + ${js.deps.length} JS + ${extra.deps.length} extra dependencies)`)
}

// Importable for tests (generate-notice.test.mjs exercises the pure helpers);
// only the direct `bun scripts/generate-notice.mjs` invocation touches the tree.
if (import.meta.main)
  await generateNotice()
