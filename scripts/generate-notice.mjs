#!/usr/bin/env bun
// generate-notice.mjs — Generate NOTICE.md and NOTICE.html with third-party
// license texts.
//
// Usage: bun scripts/generate-notice.mjs
//
// Collects licenses from:
//   - Go modules (via go.work workspace: backend + desktop)
//   - JavaScript runtime dependencies (frontend/node_modules)

import process from 'node:process'
import { execSync } from 'node:child_process'
import { existsSync, readdirSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'

const ROOT = resolve(import.meta.dirname, '..')
const FRONTEND = join(ROOT, 'frontend')
const LICENSE_OVERRIDES = join(ROOT, 'scripts/license-overrides')

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
  while (current.startsWith(stopAt)) {
    try {
      for (const entry of readdirSync(current, { withFileTypes: true })) {
        if (LICENSE_NAMES_RE.test(entry.name) && entry.isFile())
          return join(current, entry.name)
      }
    } catch { /* directory may not exist or be readable */ }
    const parent = dirname(current)
    if (parent === current) break
    current = parent
  }
  return null
}

/** Normalize license text: strip \r, trim blank lines, remove triple+ backticks. */
function normalizeLicenseText(text) {
  const lines = text.replace(/\r/g, '').split('\n')
  let start = 0
  while (start < lines.length && lines[start].trim() === '') start++
  let end = lines.length - 1
  while (end >= start && lines[end].trim() === '') end--
  return lines.slice(start, end + 1)
    .map(line => line.replace(/`{3,}/g, ''))
    .join('\n')
}

/** Format a JS dependency heading with license name. */
function jsHeading(dep) {
  return dep.license ? `${dep.name} ${dep.version} (${dep.license})` : `${dep.name} ${dep.version}`
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
  const errors = []

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
      const overrideDir = join(LICENSE_OVERRIDES, pkgName)
      if (existsSync(join(overrideDir, 'expected.json'))) {
        warnings.push(`JS: ${pkgName}@${version} — upstream now ships a LICENSE file; override in scripts/license-overrides/${pkgName}/ can be removed`)
      }
    } else {
      // No license file — check overrides.
      const overrideDir = join(LICENSE_OVERRIDES, pkgName)
      const expectedPath = join(overrideDir, 'expected.json')
      if (existsSync(expectedPath)) {
        const expected = JSON.parse(readFileSync(expectedPath, 'utf-8'))
        if (expected.license !== licenseField) {
          errors.push(`JS: ${pkgName}@${version} — license field changed from "${expected.license}" to "${licenseField}"; review and update the override in scripts/license-overrides/${pkgName}/`)
          continue
        }
        licFile = findLicenseFile(overrideDir, overrideDir)
      }
    }

    if (!licFile) {
      errors.push(`JS: ${pkgName}@${version} — no license file found; add an override in scripts/license-overrides/${pkgName}/`)
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
// Generate NOTICE.md
// ---------------------------------------------------------------------------

function buildMarkdown(goDeps, jsDeps) {
  const lines = []

  lines.push('# Third-Party Licenses')
  lines.push('')
  lines.push('This file lists the licenses of third-party dependencies used by LeapMux.')
  lines.push('')

  // Table of contents
  lines.push('## Table of Contents')
  lines.push('')
  if (goDeps.length > 0) {
    lines.push('### Go Dependencies')
    lines.push('')
    for (const dep of goDeps) {
      const heading = `${dep.name} ${dep.version}`
      lines.push(`- [${heading}](#${toAnchor(heading)})`)
    }
    lines.push('')
  }
  if (jsDeps.length > 0) {
    lines.push('### JavaScript Dependencies')
    lines.push('')
    for (const dep of jsDeps) {
      const heading = jsHeading(dep)
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
      lines.push(`### ${dep.name} ${dep.version}`)
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
      lines.push(`### ${jsHeading(dep)}`)
      lines.push('')
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
  const js = collectJsDeps()
  const allWarnings = [...go.warnings, ...js.warnings]
  const allErrors = [...go.errors, ...js.errors]

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

  const markdown = buildMarkdown(go.deps, js.deps)

  const mdPath = join(ROOT, 'NOTICE.md')
  writeFileSync(mdPath, markdown, 'utf-8')
  console.log(`✓ Written ${mdPath}`)

  console.log('Rendering HTML …')
  const html = await buildHtml(markdown)
  const htmlPath = join(ROOT, 'NOTICE.html')
  writeFileSync(htmlPath, html, 'utf-8')
  console.log(`✓ Written ${htmlPath}`)

  console.log(`  (${go.deps.length} Go + ${js.deps.length} JS dependencies)`)
}

await generateNotice()
