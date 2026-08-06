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

import process from 'node:process'
import { execSync } from 'node:child_process'
import { existsSync, readdirSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'

const ROOT = resolve(import.meta.dirname, '..')
const FRONTEND = join(ROOT, 'frontend')
const DESKTOP_RUST = join(ROOT, 'desktop/rust')
const LICENSE_OVERRIDES_GO = join(ROOT, 'scripts/license-overrides/go')
const LICENSE_OVERRIDES_RUST = join(ROOT, 'scripts/license-overrides/rust')
const LICENSE_OVERRIDES_JS = join(ROOT, 'scripts/license-overrides/js')
const LICENSE_EXTRAS = join(ROOT, 'scripts/license-overrides/extra')
// Canonical license texts for crates that declare an SPDX term but ship no
// license file of their own. Each entry names a crate that *is* in the
// dependency graph and does ship that term's text; the directory is resolved
// from `cargo metadata` at run time (see createRustSpdxResolver), so a
// `cargo update` moves these with the lock instead of stranding a hardcoded
// version-stamped path.
//
// `signature` is a phrase the resolved file must contain. Existence alone is
// not enough: the BSD-3-Clause entry pointed at zerocopy's LICENSE-BSD for
// years, which is a *two*-clause text, so every crate that borrowed it
// reproduced the wrong license in NOTICE and only a human reading it caught
// on. Checking for a term-specific phrase turns that into a build failure.
//
// Pick a source whose copyright line is defensible for the crates that will
// borrow it -- alloc-no-stdlib is alloc-stdlib's sibling under the same
// Dropbox BSD-3-Clause grant, so its text is the right one to reproduce.
export const RUST_SPDX_LICENSE_SOURCES = {
  'Apache-2.0': { crate: 'anyhow', file: 'LICENSE-APACHE', signature: 'Apache License' },
  'Apache-2.0 WITH LLVM-exception': { crate: 'rustix', file: 'LICENSE-Apache-2.0_WITH_LLVM-exception', signature: 'LLVM Exception' },
  'BSD-3-Clause': { crate: 'alloc-no-stdlib', file: 'LICENSE', signature: 'Neither the name' },
  MIT: { crate: 'anyhow', file: 'LICENSE-MIT', signature: 'Permission is hereby granted' },
  'MPL-2.0': { crate: 'option-ext', file: 'LICENSE.txt', signature: 'Mozilla Public License' },
  Zlib: { crate: 'bytemuck', file: 'LICENSE-ZLIB', signature: 'Altered source versions must be plainly marked' },
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Names (case-insensitive) that indicate a license file. */
const LICENSE_NAMES_RE = /^(licen[cs]e|copying|notice|unlicense)([-_. ].+)?$/i

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
      if (matches.length > 0) return matches.sort()
    } catch { /* directory may not exist or be readable */ }
    const parent = dirname(current)
    if (parent === current) break
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
  const base = dep.version ? `${dep.name} ${dep.version}` : dep.name
  return dep.license ? `${base} (${dep.license})` : base
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
    const core = dash === -1 ? version : version.slice(0, dash)
    const prerelease = dash === -1 ? '' : version.slice(dash + 1)
    // Build metadata ("0.6.0+11769913") carries no precedence; drop it.
    const plus = core.indexOf('+')
    const numbers = (plus === -1 ? core : core.slice(0, plus)).split('.').map(Number)
    return { numbers, prerelease }
  }

  const left = split(a)
  const right = split(b)
  for (let i = 0; i < Math.max(left.numbers.length, right.numbers.length); i++) {
    const delta = (left.numbers[i] ?? 0) - (right.numbers[i] ?? 0)
    if (delta !== 0) return delta
  }
  // Same core version: a release outranks any pre-release of it.
  if (left.prerelease === right.prerelease) return 0
  if (left.prerelease === '') return 1
  if (right.prerelease === '') return -1
  return left.prerelease < right.prerelease ? -1 : 1
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
  const sourceCrates = new Set(Object.values(RUST_SPDX_LICENSE_SOURCES).map(source => source.crate))

  /** @type {Map<string, {version: string, dir: string}>} */
  const crateDirs = new Map()
  for (const pkg of packages) {
    if (!sourceCrates.has(pkg.name)) continue
    // A crate can appear at more than one version; take the highest so the
    // chosen text never depends on the order `cargo metadata` emits.
    const previous = crateDirs.get(pkg.name)
    if (previous && compareCrateVersions(previous.version, pkg.version) >= 0) continue
    crateDirs.set(pkg.name, { version: pkg.version, dir: dirname(pkg.manifest_path) })
  }

  const cache = new Map()
  return function resolveSpdxText(term) {
    if (!cache.has(term)) cache.set(term, loadSpdxText(term, crateDirs))
    return cache.get(term)
  }
}

/** Read and validate one SPDX term's canonical text. @returns {{text: string} | {error: string}} */
export function loadSpdxText(term, crateDirs) {
  const { crate, file, signature } = RUST_SPDX_LICENSE_SOURCES[term]
  const source = crateDirs.get(crate)
  if (!source) {
    return {
      error: `canonical "${term}" text: crate ${crate} is no longer in desktop/rust/Cargo.lock. `
        + 'Point RUST_SPDX_LICENSE_SOURCES at a crate that is, and that ships this license text.',
    }
  }

  const path = join(source.dir, file)
  if (!existsSync(path)) {
    return { error: `canonical "${term}" text: ${crate} ${source.version} no longer ships ${file} (looked in ${source.dir}).` }
  }

  const text = normalizeLicenseText(readFileSync(path, 'utf-8'))
  if (!text.includes(signature)) {
    return {
      error: `canonical "${term}" text: ${crate} ${source.version} ${file} does not read like ${term} `
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
 * @returns {{text: string | null, failures: Array<{term: string, message: string}>}}
 */
export function collectRustSpdxTexts(licenseExpression, resolveSpdxText) {
  if (!licenseExpression) return { text: null, failures: [] }

  const normalized = licenseExpression.replace(/\//g, ' OR ')
  // "MIT OR Apache-2.0" needs only one term satisfied; anything with an AND
  // needs all of them.
  const isChoice = normalized.includes(' OR ') && !normalized.includes(' AND ')
  const terms = [...new Set(
    normalized
      .split(/\s+(?:OR|AND)\s+/)
      .map(term => term.replace(/[()]/g, '').trim())
      .filter(Boolean),
  )]

  const texts = []
  const failures = []
  let unregistered = 0
  for (const term of terms) {
    if (!(term in RUST_SPDX_LICENSE_SOURCES)) {
      unregistered++
      continue
    }
    const resolved = resolveSpdxText(term)
    if (resolved.error) failures.push({ term, message: resolved.error })
    else texts.push(resolved.text)
  }

  const satisfied = isChoice
    ? texts.length > 0
    : unregistered === 0 && failures.length === 0 && texts.length > 0
  return { text: satisfied ? texts.join('\n\n-----\n\n') : null, failures }
}

/** Slugify a heading for use as a markdown anchor. */
export function toAnchor(heading) {
  return heading
    .toLowerCase()
    .replace(/[^a-z0-9 _-]/g, '')
    .replace(/\s+/g, '-')
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
      if (esc) esc = false
      else if (c === '\\') esc = true
      else if (c === '"') inStr = false
      continue
    }
    if (c === '"') { inStr = true; continue }
    if (c === '{') {
      if (depth === 0) start = i
      depth++
    } else if (c === '}') {
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
      const raw = execSync('go list -deps -test -json -tags integration ./...', {
        cwd,
        encoding: 'utf-8',
        env: { ...process.env, GOOS: t.GOOS, GOARCH: t.GOARCH },
        maxBuffer: 128 * 1024 * 1024,
      })
      for (const pkg of parseGoListJson(raw)) {
        const m = pkg.Module
        if (m && !m.Main) keys.add(`${m.Path}@${m.Version}`)
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

  // Determine Go module cache root for walking up parent dirs.
  const goModCache = execSync('go env GOMODCACHE', { cwd: ROOT, encoding: 'utf-8' }).trim()

  for (const mod of modules) {
    if (mod.Main) continue
    const key = `${mod.Path}@${mod.Version}`
    if (deps.has(key)) continue
    if (!runtimeKeys.has(key)) continue // skip tool-only deps

    let dir = mod.Dir
    if (!dir) {
      // Fallback: construct the expected cache path.
      dir = join(goModCache, mod.Path + '@' + mod.Version)
      if (!existsSync(dir)) {
        warnings.push(`Go: ${key} — no Dir and cache miss`)
        continue
      }
    }

    let licFile = findLicenseFile(dir, goModCache)
    if (!licFile) {
      // Check for a manual override in scripts/license-overrides/go/.
      // Try the module path with slashes replaced by dashes.
      const overrideName = mod.Path.replace(/\//g, '-')
      const overrideDir = join(LICENSE_OVERRIDES_GO, overrideName)
      if (existsSync(join(overrideDir, 'expected.json'))) {
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

  return { deps: [...deps.values()].sort((a, b) => a.name.localeCompare(b.name)), warnings, errors }
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

  for (const pkg of metadata.packages ?? []) {
    if (pkg.id === metadata.resolve?.root || pkg.source == null) continue

    const key = `${pkg.name}@${pkg.version}`
    if (deps.has(key)) continue

    const manifestDir = dirname(pkg.manifest_path)

    // One walk of the manifest directory, reused: findLicenseFile is just
    // findLicenseFiles(...)[0], so asking for both re-scanned the same
    // directory for ~490 of the graph's crates.
    const manifestLicenses = findLicenseFiles(manifestDir, manifestDir)

    // `license_file` names a file Cargo.toml points at explicitly; no crate in
    // the current graph sets it, but honour it ahead of discovery when one does.
    const declared = pkg.license_file ? join(manifestDir, pkg.license_file) : null

    /** @type {string[]} */
    let licenseFiles = []
    if (declared && existsSync(declared)) licenseFiles = [declared]
    else if (manifestLicenses.length > 0) licenseFiles = manifestLicenses
    else {
      // Nothing on disk: fall back to a hand-curated override directory.
      const overrideDir = join(LICENSE_OVERRIDES_RUST, pkg.name)
      if (existsSync(join(overrideDir, 'expected.json'))) {
        licenseFiles = findLicenseFiles(overrideDir, overrideDir)
      }
    }

    let licenseText = null
    if (licenseFiles.length > 0) {
      licenseText = licenseFiles.map(path => normalizeLicenseText(readFileSync(path, 'utf-8'))).join('\n\n-----\n\n')
    } else {
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

  return { deps: [...deps.values()].sort((a, b) => a.name.localeCompare(b.name)), warnings: [], errors }
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

  const nodeModules = join(FRONTEND, 'node_modules')
  if (!existsSync(nodeModules)) {
    warnings.push('JS: node_modules not found — run `bun install` first')
    return { deps, warnings, errors }
  }

  // Collect package directories (flat + scoped).
  /** @type {Array<{pkgDir: string, pkgName: string}>} */
  const packages = []

  for (const entry of readdirSync(nodeModules)) {
    if (entry.startsWith('.')) continue
    const full = join(nodeModules, entry)
    if (entry.startsWith('@')) {
      // Scoped package — enumerate children.
      try {
        for (const child of readdirSync(full)) {
          if (child.startsWith('.')) continue
          packages.push({ pkgDir: join(full, child), pkgName: `${entry}/${child}` })
        }
      } catch { /* ignore */ }
    } else {
      packages.push({ pkgDir: full, pkgName: entry })
    }
  }

  for (const { pkgDir, pkgName } of packages) {
    if (!runtimeDeps.has(pkgName)) continue

    let meta
    try {
      meta = JSON.parse(readFileSync(join(pkgDir, 'package.json'), 'utf-8'))
    } catch {
      continue
    }

    const version = meta.version ?? 'unknown'
    const licenseField = meta.license ?? 'unknown'
    let licFile = findLicenseFile(pkgDir, nodeModules)

    if (licFile) {
      // Upstream ships a license file. Warn if we still have an override for it.
      const overrideDir = join(LICENSE_OVERRIDES_JS, pkgName)
      if (existsSync(join(overrideDir, 'expected.json'))) {
        warnings.push(`JS: ${pkgName}@${version} — upstream now ships a LICENSE file; override in scripts/license-overrides/js/${pkgName}/ can be removed`)
      }
    } else {
      // No license file — check overrides.
      const overrideDir = join(LICENSE_OVERRIDES_JS, pkgName)
      const expectedPath = join(overrideDir, 'expected.json')
      if (existsSync(expectedPath)) {
        const expected = JSON.parse(readFileSync(expectedPath, 'utf-8'))
        if (expected.license !== licenseField) {
          errors.push(`JS: ${pkgName}@${version} — license field changed from "${expected.license}" to "${licenseField}"; review and update the override in scripts/license-overrides/js/${pkgName}/`)
          continue
        }
        licFile = findLicenseFile(overrideDir, overrideDir)
      }
    }

    if (!licFile) {
      errors.push(`JS: ${pkgName}@${version} — no license file found; add an override in scripts/license-overrides/js/${pkgName}/`)
      continue
    }

    deps.push({
      name: pkgName,
      version,
      license: licenseField,
      licenseText: normalizeLicenseText(readFileSync(licFile, 'utf-8')),
    })
  }

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
    if (!entry.isDirectory() || entry.name.startsWith('.')) continue
    const dir = join(LICENSE_EXTRAS, entry.name)
    const metaPath = join(dir, 'metadata.json')

    if (!existsSync(metaPath)) {
      errors.push(`Extra: ${entry.name} — missing metadata.json`)
      continue
    }

    let meta
    try {
      meta = JSON.parse(readFileSync(metaPath, 'utf-8'))
    } catch (err) {
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
      version: typeof meta.version === 'string' && meta.version.length > 0 ? meta.version : null,
      license: meta.license,
      url: typeof meta.url === 'string' && meta.url.length > 0 ? meta.url : null,
      description: typeof meta.description === 'string' && meta.description.length > 0 ? meta.description : null,
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

  // Table of contents
  lines.push('## Table of Contents')
  lines.push('')
  if (goDeps.length > 0) {
    lines.push('### Go Dependencies')
    lines.push('')
    for (const dep of goDeps) {
      const heading = depHeading(dep)
      lines.push(`- [${heading}](#${toAnchor(heading)})`)
    }
    lines.push('')
  }
  if (rustDeps.length > 0) {
    lines.push('### Rust Dependencies')
    lines.push('')
    for (const dep of rustDeps) {
      const heading = depHeading(dep)
      lines.push(`- [${heading}](#${toAnchor(heading)})`)
    }
    lines.push('')
  }
  if (jsDeps.length > 0) {
    lines.push('### JavaScript Dependencies')
    lines.push('')
    for (const dep of jsDeps) {
      const heading = depHeading(dep)
      lines.push(`- [${heading}](#${toAnchor(heading)})`)
    }
    lines.push('')
  }
  if (extraDeps.length > 0) {
    lines.push('### Other Third-Party Notices')
    lines.push('')
    for (const dep of extraDeps) {
      const heading = depHeading(dep)
      lines.push(`- [${heading}](#${toAnchor(heading)})`)
    }
    lines.push('')
  }

  // Go dependencies
  if (goDeps.length > 0) {
    lines.push('---')
    lines.push('')
    lines.push('## Go Dependencies')
    lines.push('')
    for (const dep of goDeps) {
      lines.push(`### ${depHeading(dep)}`)
      lines.push('')
      lines.push('```')
      lines.push(dep.licenseText)
      lines.push('```')
      lines.push('')
    }
  }

  // Rust dependencies
  if (rustDeps.length > 0) {
    lines.push('---')
    lines.push('')
    lines.push('## Rust Dependencies')
    lines.push('')
    for (const dep of rustDeps) {
      lines.push(`### ${depHeading(dep)}`)
      lines.push('')
      lines.push('```')
      lines.push(dep.licenseText)
      lines.push('```')
      lines.push('')
    }
  }

  // JavaScript dependencies
  if (jsDeps.length > 0) {
    lines.push('---')
    lines.push('')
    lines.push('## JavaScript Dependencies')
    lines.push('')
    for (const dep of jsDeps) {
      lines.push(`### ${depHeading(dep)}`)
      lines.push('')
      lines.push('```')
      lines.push(dep.licenseText)
      lines.push('```')
      lines.push('')
    }
  }

  // Other third-party notices (vendored / manually listed)
  if (extraDeps.length > 0) {
    lines.push('---')
    lines.push('')
    lines.push('## Other Third-Party Notices')
    lines.push('')
    lines.push('The following items are not tracked by any package manager — their')
    lines.push('sources were copied or adapted into this repository. They are listed')
    lines.push('separately to make their provenance explicit.')
    lines.push('')
    for (const dep of extraDeps) {
      lines.push(`### ${depHeading(dep)}`)
      lines.push('')
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

  const bodyHtml = String(
    await unified()
      .use(remarkParse)
      .use(remarkGfm)
      .use(remarkRehype)
      .use(rehypeStringify)
      .process(markdown),
  )

  // Read Oat CSS to inline.
  const oatCss = readFileSync(join(FRONTEND, 'node_modules/@knadh/oat/oat.min.css'), 'utf-8')

  // LeapMux theme overrides (extracted from frontend/src/styles/global.css.ts).
  const themeCss = `
/* LeapMux light theme */
:root {
  --background: rgb(253 252 250);
  --foreground: rgb(34 32 30);
  --card: rgb(247 245 242);
  --card-foreground: rgb(34 32 30);
  --primary: rgb(13 148 136);
  --primary-foreground: rgb(255 255 255);
  --secondary: rgb(232 230 225);
  --secondary-foreground: rgb(46 43 40);
  --muted: rgb(237 235 231);
  --muted-foreground: rgb(120 117 111);
  --faint: rgb(242 240 236);
  --faint-foreground: rgb(160 157 151);
  --accent: rgb(222 235 225);
  --accent-foreground: rgb(34 32 30);
  --border: rgb(221 217 211);
  --input: rgb(213 209 203);
  --ring: rgb(13 148 136);
  --font-sans: system-ui, sans-serif;
  --font-mono: "SF Mono", Consolas, monospace;
}

/* LeapMux dark theme */
@media (prefers-color-scheme: dark) {
  :root {
    --background: rgb(26 25 23);
    --foreground: rgb(232 230 225);
    --card: rgb(42 40 38);
    --card-foreground: rgb(232 230 225);
    --primary: rgb(20 184 166);
    --primary-foreground: rgb(12 12 11);
    --secondary: rgb(51 48 45);
    --secondary-foreground: rgb(224 221 216);
    --muted: rgb(46 43 40);
    --muted-foreground: rgb(138 134 128);
    --faint: rgb(36 34 32);
    --faint-foreground: rgb(107 104 98);
    --accent: rgb(45 62 50);
    --accent-foreground: rgb(232 230 225);
    --border: rgb(61 58 54);
    --input: rgb(61 58 54);
    --ring: rgb(20 184 166);
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
  const allWarnings = [...go.warnings, ...rust.warnings, ...js.warnings, ...extra.warnings]
  const allErrors = [...go.errors, ...rust.errors, ...js.errors, ...extra.errors]

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
if (import.meta.main) await generateNotice()
