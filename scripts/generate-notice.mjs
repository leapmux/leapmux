#!/usr/bin/env bun
// generate-notice.mjs — Generate NOTICE.md with third-party license texts.
//
// Usage: bun scripts/generate-notice.mjs
//
// Collects licenses from:
//   - Go modules (via go.work workspace: backend + desktop)
//   - JavaScript runtime dependencies (frontend/node_modules)

import { execSync } from 'node:child_process'
import { existsSync, readdirSync, readFileSync, statSync } from 'node:fs'
import { writeFileSync } from 'node:fs'
import { basename, dirname, join, resolve } from 'node:path'

const ROOT = resolve(import.meta.dirname, '..')
const FRONTEND = join(ROOT, 'frontend')

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Names (case-insensitive) that indicate a license file. */
const LICENSE_NAMES_RE = /^(licen[cs]e|copying|notice)(\..+)?$/i

/**
 * Find a license file in `dir`, optionally walking up parent directories
 * until `stopAt` (exclusive). Returns the file path or null.
 */
function findLicenseFile(dir, stopAt) {
  let current = dir
  while (current && current.length >= (stopAt ?? '').length) {
    try {
      const entries = readdirSync(current)
      for (const entry of entries) {
        if (LICENSE_NAMES_RE.test(entry)) {
          const full = join(current, entry)
          if (statSync(full).isFile()) return full
        }
      }
    } catch {
      // directory may not exist or be readable
    }
    const parent = dirname(current)
    if (parent === current) break
    current = parent
  }
  return null
}

/** Slugify a heading for use as a markdown anchor. */
function toAnchor(heading) {
  return heading
    .toLowerCase()
    .replace(/[^a-z0-9 _-]/g, '')
    .replace(/\s+/g, '-')
}

// ---------------------------------------------------------------------------
// Go dependencies
// ---------------------------------------------------------------------------

function collectGoDeps() {
  // Ensure all modules are downloaded so Dir fields are populated.
  console.log('Downloading Go modules …')
  execSync('go mod download', { cwd: ROOT, stdio: 'inherit' })

  console.log('Listing Go modules …')
  const raw = execSync('go list -m -json all', { cwd: ROOT, encoding: 'utf-8' })

  // Parse the concatenated JSON objects.
  const modules = []
  let depth = 0
  let start = -1
  for (let i = 0; i < raw.length; i++) {
    if (raw[i] === '{') {
      if (depth === 0) start = i
      depth++
    } else if (raw[i] === '}') {
      depth--
      if (depth === 0 && start >= 0) {
        modules.push(JSON.parse(raw.slice(start, i + 1)))
        start = -1
      }
    }
  }

  /** @type {Map<string, {name: string, version: string, licenseText: string}>} */
  const deps = new Map()
  const warnings = []

  // Determine Go module cache root for walking up parent dirs.
  const goModCache = execSync('go env GOMODCACHE', { cwd: ROOT, encoding: 'utf-8' }).trim()

  for (const mod of modules) {
    if (mod.Main) continue
    const key = `${mod.Path}@${mod.Version}`
    if (deps.has(key)) continue

    let dir = mod.Dir
    if (!dir) {
      // Fallback: construct the expected cache path.
      dir = join(goModCache, mod.Path + '@' + mod.Version)
      if (!existsSync(dir)) {
        warnings.push(`Go: ${key} — no Dir and cache miss`)
        continue
      }
    }

    const licFile = findLicenseFile(dir, goModCache)
    if (!licFile) {
      warnings.push(`Go: ${key} — no license file found in ${dir}`)
      deps.set(key, { name: mod.Path, version: mod.Version, licenseText: '*License file not found.*' })
      continue
    }

    deps.set(key, {
      name: mod.Path,
      version: mod.Version,
      licenseText: readFileSync(licFile, 'utf-8').trimEnd(),
    })
  }

  return { deps: [...deps.values()].sort((a, b) => a.name.localeCompare(b.name)), warnings }
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

  const nodeModules = join(FRONTEND, 'node_modules')
  if (!existsSync(nodeModules)) {
    warnings.push('JS: node_modules not found — run `bun install` first')
    return { deps, warnings }
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

    const childPkgJson = join(pkgDir, 'package.json')
    if (!existsSync(childPkgJson)) continue

    let meta
    try {
      meta = JSON.parse(readFileSync(childPkgJson, 'utf-8'))
    } catch {
      continue
    }

    const version = meta.version ?? 'unknown'
    const licFile = findLicenseFile(pkgDir, nodeModules)
    if (!licFile) {
      warnings.push(`JS: ${pkgName}@${version} — no license file found`)
      deps.push({ name: pkgName, version, licenseText: `License: ${meta.license ?? 'unknown'}\n\n*License file not found.*` })
      continue
    }

    deps.push({
      name: pkgName,
      version,
      licenseText: readFileSync(licFile, 'utf-8').trimEnd(),
    })
  }

  deps.sort((a, b) => a.name.localeCompare(b.name))
  return { deps, warnings }
}

// ---------------------------------------------------------------------------
// Generate NOTICE.md
// ---------------------------------------------------------------------------

function generateNotice() {
  const go = collectGoDeps()
  const js = collectJsDeps()
  const allWarnings = [...go.warnings, ...js.warnings]

  if (allWarnings.length > 0) {
    console.warn('\nWarnings:')
    for (const w of allWarnings) console.warn(`  ⚠ ${w}`)
    console.warn()
  }

  const lines = []

  lines.push('# Third-Party Licenses')
  lines.push('')
  lines.push('This file lists the licenses of third-party dependencies used by LeapMux.')
  lines.push('')

  // Table of contents
  lines.push('## Table of Contents')
  lines.push('')
  if (go.deps.length > 0) {
    lines.push('### Go Dependencies')
    lines.push('')
    for (const dep of go.deps) {
      const heading = `${dep.name} ${dep.version}`
      lines.push(`- [${heading}](#${toAnchor(heading)})`)
    }
    lines.push('')
  }
  if (js.deps.length > 0) {
    lines.push('### JavaScript Dependencies')
    lines.push('')
    for (const dep of js.deps) {
      const heading = `${dep.name} ${dep.version}`
      lines.push(`- [${heading}](#${toAnchor(heading)})`)
    }
    lines.push('')
  }

  // Go dependencies
  if (go.deps.length > 0) {
    lines.push('---')
    lines.push('')
    lines.push('## Go Dependencies')
    lines.push('')
    for (const dep of go.deps) {
      lines.push(`### ${dep.name} ${dep.version}`)
      lines.push('')
      lines.push('```')
      lines.push(dep.licenseText)
      lines.push('```')
      lines.push('')
    }
  }

  // JavaScript dependencies
  if (js.deps.length > 0) {
    lines.push('---')
    lines.push('')
    lines.push('## JavaScript Dependencies')
    lines.push('')
    for (const dep of js.deps) {
      lines.push(`### ${dep.name} ${dep.version}`)
      lines.push('')
      lines.push('```')
      lines.push(dep.licenseText)
      lines.push('```')
      lines.push('')
    }
  }

  const content = lines.join('\n')
  const outPath = join(ROOT, 'NOTICE.md')
  writeFileSync(outPath, content, 'utf-8')
  console.log(`✓ Written ${outPath} (${go.deps.length} Go + ${js.deps.length} JS dependencies)`)
}

generateNotice()
