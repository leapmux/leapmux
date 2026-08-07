#!/usr/bin/env bun
// sync-versions.mjs — Make every hand-written copy of a toolchain version agree
// with versions.env, the single source of truth.
//
// Usage:
//   bun scripts/sync-versions.mjs            # rewrite every claim site in place
//   bun scripts/sync-versions.mjs --check    # report drift and exit 1 (CI/lint)
//
// versions.env is already *sourced* by everything that can source a file:
// Taskfile.yaml reads it via `dotenv:`, the workflows splat it into $GITHUB_ENV,
// and docker/Dockerfile takes it as build ARGs. What none of those can reach is
// prose in README.md and the docs site, or the `go` directive in a go.mod --
// formats with nowhere to put a variable. Those copies were hand-edited on every
// bump, which is why the docs site sat on a Go version two releases stale while
// the README was current.
//
// So this script closes the loop from the other side: versions.env stays the
// source, and every unreachable copy is REWRITTEN from it rather than retyped.
// `--check` runs in `task lint`, so a copy that drifts fails the build instead
// of shipping.
//
// Adding a claim: append to CLAIMS below. A claim whose pattern matches nothing
// is a hard error, not a skip -- see the note on that check.

import { readFileSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'

const ROOT = resolve(import.meta.dirname, '..')
const VERSIONS_ENV = join(ROOT, 'versions.env')

/** The major component of a semver-ish version string: "24.19.0" -> "24". */
export function major(version) {
  return version.split('.')[0]
}

/**
 * Every hand-written copy of a versions.env value, and how to rewrite it.
 *
 * - `file`    repo-relative path.
 * - `key`     the versions.env key that owns this text.
 * - `pattern` matches the WHOLE line (or fragment) to be replaced. It must
 *             carry exactly one capture group around the version itself, so a
 *             drift report can name what the file currently claims. Anchor it
 *             tightly enough that it cannot match something it shouldn't.
 * - `render`  builds the replacement from the versions.env value. Its output
 *             must itself match `pattern`, or the file would never converge.
 *             `applyClaim` enforces that BEFORE writing anything: see
 *             `assertRoundTrips`.
 *
 * A `go` directive is a language floor rather than a toolchain pin, but this
 * repo has always moved the two together (all four modules track GOLANG_VERSION
 * exactly), so they are claims like any other. Splitting them would mean a
 * second source of truth for the same number.
 */
export const CLAIMS = [
  {
    file: 'README.md',
    key: 'GOLANG_VERSION',
    pattern: /^- \*\*Go\*\* (\S+) or later$/gm,
    render: v => `- **Go** ${v} or later`,
  },
  {
    file: 'README.md',
    key: 'NODE_VERSION',
    pattern: /^- \*\*Node\.js\*\* (\S+) or later$/gm,
    render: v => `- **Node.js** ${major(v)} or later`,
  },
  {
    file: 'site/content/docs/getting-started/installation.md',
    key: 'GOLANG_VERSION',
    pattern: /^- \*\*Go\*\* (\S+) or later$/gm,
    render: v => `- **Go** ${v} or later`,
  },
  {
    file: 'site/content/docs/getting-started/installation.md',
    key: 'NODE_VERSION',
    pattern: /^- \*\*Node\.js\*\* (\S+) or later$/gm,
    render: v => `- **Node.js** ${major(v)} or later`,
  },
  {
    file: 'go.work',
    key: 'GOLANG_VERSION',
    pattern: /^go (\S+)$/gm,
    render: v => `go ${v}`,
  },
  {
    file: 'backend/go.mod',
    key: 'GOLANG_VERSION',
    pattern: /^go (\S+)$/gm,
    render: v => `go ${v}`,
  },
  {
    file: 'desktop/go/go.mod',
    key: 'GOLANG_VERSION',
    pattern: /^go (\S+)$/gm,
    render: v => `go ${v}`,
  },
  {
    file: 'site/go.mod',
    key: 'GOLANG_VERSION',
    pattern: /^go (\S+)$/gm,
    render: v => `go ${v}`,
  },
]

// ---------------------------------------------------------------------------
// versions.env
// ---------------------------------------------------------------------------

/**
 * Parse versions.env text into a `KEY -> value` Map.
 *
 * Deliberately the same trivial grammar the workflows assume when they grep
 * `^[A-Z_][A-Z0-9_]*=` into $GITHUB_ENV: plain KEY=VALUE lines, `#` comments,
 * blank lines, no quoting and no multi-line values. Anything richer would have
 * to be taught to every consumer, which is the point the file's own header
 * makes.
 *
 * @param {string} text contents of versions.env
 * @returns {Map<string, string>} every KEY=VALUE pair, in file order
 */
export function parseVersions(text) {
  const versions = new Map()
  for (const [index, line] of text.split('\n').entries()) {
    const trimmed = line.trim()
    if (trimmed === '' || trimmed.startsWith('#'))
      continue
    const eq = trimmed.indexOf('=')
    if (eq <= 0) {
      throw new Error(`versions.env:${index + 1}: not a KEY=VALUE line: ${trimmed}`)
    }
    versions.set(trimmed.slice(0, eq), trimmed.slice(eq + 1))
  }
  return versions
}

// ---------------------------------------------------------------------------
// Claims
// ---------------------------------------------------------------------------

/** The 1-based line number containing `index` in `text`. */
export function lineAt(text, index) {
  let line = 1
  for (let i = 0; i < index; i++) {
    if (text[i] === '\n')
      line++
  }
  return line
}

/**
 * Fail unless `replacement` is something `claim.pattern` would match back.
 *
 * Without this the script happily writes a value that its own pattern cannot
 * find again. `GOLANG_VERSION=` (a botched bump that leaves the key empty)
 * renders as a bare `go `, which is written into go.work and all three go.mod
 * files; every Go command then dies with `invalid go version ""`, and the next
 * run throws "no line matches the GOLANG_VERSION claim" -- blaming the prose
 * for a line this tool wrote. The same holds for any value the pattern cannot
 * round-trip, such as one carrying an inline comment or a space.
 *
 * The pattern is `/g` for `matchAll`, so test against a non-global clone;
 * requiring the match to span the whole replacement also rejects a multi-line
 * value whose first line happens to match under `/m`.
 */
function assertRoundTrips(claim, replacement, want) {
  const whole = new RegExp(claim.pattern.source, claim.pattern.flags.replace('g', ''))
  if (whole.exec(replacement)?.[0] !== replacement) {
    throw new Error(
      `${claim.file}: ${claim.key}=${JSON.stringify(want)} renders as ${JSON.stringify(replacement)}, `
      + `which its own claim pattern ${claim.pattern} would not match back. `
      + 'Fix the value in versions.env, or the claim in scripts/sync-versions.mjs.',
    )
  }
}

/**
 * Apply one claim to already-read file content.
 *
 * Returns the rewritten content plus a drift entry per site that disagreed.
 * Every match is rewritten, not just the first: a file is free to state the
 * same requirement twice, and half-rewriting it would be worse than not
 * rewriting it at all.
 */
export function applyClaim(claim, content, want) {
  const drifts = []
  const matches = [...content.matchAll(claim.pattern)]

  // A claim that matches nothing is the failure mode this whole script exists
  // to prevent, one level up: the prose was reworded, the claim silently
  // stopped being enforced, and the next bump leaves the file stale with a
  // green build. Fail loudly instead.
  if (matches.length === 0) {
    throw new Error(
      `${claim.file}: no line matches the ${claim.key} claim ${claim.pattern}. `
      + 'The text was reworded or moved -- update the pattern in scripts/sync-versions.mjs, '
      + 'or drop the claim if the file no longer states that version.',
    )
  }

  const replacement = claim.render(want)
  assertRoundTrips(claim, replacement, want)

  let rewritten = ''
  let cursor = 0
  for (const match of matches) {
    if (match[0] !== replacement) {
      drifts.push({
        file: claim.file,
        line: lineAt(content, match.index),
        key: claim.key,
        found: match[1],
        expected: replacement,
      })
    }
    rewritten += content.slice(cursor, match.index) + replacement
    cursor = match.index + match[0].length
  }
  rewritten += content.slice(cursor)

  return { content: rewritten, drifts }
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

function main(argv) {
  const check = argv.includes('--check')
  const unknown = argv.filter(a => a !== '--check')
  if (unknown.length > 0) {
    process.stderr.write(`sync-versions: unexpected argument: ${unknown[0]}\n`)
    return 2
  }

  const allDrifts = []
  let versions
  // Everything that reads the tree lives in here, versions.env included: its
  // own "not a KEY=VALUE line" error used to escape past this handler and
  // surface as a raw Bun stack trace, which is the least helpful shape for the
  // one file a contributor is most likely to have just hand-edited.
  try {
    versions = parseVersions(readFileSync(VERSIONS_ENV, 'utf-8'))

    // Group by file so a file carrying several claims is read and written once,
    // and so each claim sees the previous one's rewrite rather than the original.
    /** @type {Map<string, typeof CLAIMS>} */
    const byFile = new Map()
    for (const claim of CLAIMS) {
      if (!versions.has(claim.key)) {
        throw new Error(`${claim.file} claims ${claim.key}, which versions.env does not define`)
      }
      byFile.set(claim.file, [...(byFile.get(claim.file) ?? []), claim])
    }

    // Compute every rewrite before performing any of them. A claim that cannot
    // round-trip throws, and a tree with a rewritten README beside an untouched
    // go.mod is worse than one nothing touched.
    const pending = []
    for (const [file, claims] of byFile) {
      const path = join(ROOT, file)
      const original = readFileSync(path, 'utf-8')
      let content = original
      for (const claim of claims) {
        const result = applyClaim(claim, content, versions.get(claim.key))
        content = result.content
        allDrifts.push(...result.drifts)
      }
      if (content !== original)
        pending.push({ path, content })
    }

    if (!check) {
      for (const { path, content } of pending) writeFileSync(path, content)
    }
  }
  catch (error) {
    process.stderr.write(`sync-versions: ${error.message}\n`)
    return 1
  }

  if (allDrifts.length === 0) {
    process.stdout.write(`sync-versions: ${CLAIMS.length} version claims agree with versions.env\n`)
    return 0
  }

  for (const drift of allDrifts) {
    process.stdout.write(
      `  ${check ? '✗' : '·'} ${drift.file}:${drift.line} ${check ? 'claims' : 'updated:'} ${drift.found} `
      + `(${drift.key}=${versions.get(drift.key)}) -> ${drift.expected}\n`,
    )
  }

  if (check) {
    process.stderr.write(
      `sync-versions: ${allDrifts.length} version claim(s) disagree with versions.env. `
      + 'Run `task sync-versions` to update them.\n',
    )
    return 1
  }

  process.stdout.write(`sync-versions: rewrote ${allDrifts.length} version claim(s) from versions.env\n`)
  return 0
}

// Importable for tests (sync-versions.test.mjs exercises the pure helpers);
// only the direct `bun scripts/sync-versions.mjs` invocation touches files.
if (import.meta.main)
  process.exit(main(process.argv.slice(2)))
