// Validates every project-written JSON file against a JSON Schema
// (draft 2020-12). Run via `task validate-json`.
//
// The rule is NO SCHEMALESS JSON: any file an include pattern below matches
// must resolve to a schema, either an explicit `schema` on the rule or a
// sibling `<name>.schema.json`, or this script fails. That is what keeps the
// "any other JSONs" promise honest -- a new fixture cannot appear without a
// schema saying what shape it is.
//
// Tool-owned JSON (package.json, tsconfig*, tauri*.conf.json, lockfiles,
// .webmanifest, .jsonc) is deliberately out of scope: another tool owns its
// shape, and a schema here would be a second authority that drifts. Gitignored
// build output (spinners, desktop/rust/gen) never enters an include pattern.
//
// ajv is resolved from frontend/node_modules via createRequire because the
// repo has no root package.json on purpose (see eslint.config.mjs).

import { existsSync, readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { posix, sep } from 'node:path'
import { argv, exit } from 'node:process'

/** One scope of JSON files plus how its schema is found. */
export const RULES = [
  // The single sources of truth for cross-language constants. A sibling
  // <name>.schema.json is required for each.
  { include: 'contracts/*.json' },
  // Cross-language conformance fixtures, asserted by both language's suites.
  // The two CRDT corpora share one schema (identical shape; one is
  // hand-curated, the other generated).
  {
    include: 'testdata/crdt_projection_{conformance,corpus}.json',
    schema: 'testdata/crdt_projection.schema.json',
  },
  { include: 'testdata/*.json' },
  // Package-local test fixtures (e.g. the usersettings account schema).
  { include: 'backend/**/testdata/*.json' },
  // Vendored shiki/VS Code themes: one shared schema for the family.
  {
    include: 'frontend/src/lib/syntaxThemes/*.json',
    schema: 'frontend/src/lib/syntaxThemes/syntax-theme.schema.json',
  },
  // NOTICE-generation metadata: one shared schema per file name.
  {
    include: 'scripts/license-overrides/extra/*/metadata.json',
    schema: 'scripts/license-overrides/metadata.schema.json',
  },
  {
    include: 'scripts/license-overrides/*/*/expected.json',
    schema: 'scripts/license-overrides/expected.schema.json',
  },
]

const require = createRequire(new URL('../frontend/package.json', import.meta.url))
const { default: Ajv2020 } = require('ajv/dist/2020')

/** An ajv instance configured the way every schema in this repo is written. */
export function buildAjv() {
  return new Ajv2020({ strict: true, allErrors: true })
}

/**
 * Bun's Glob.scanSync joins its results with the NATIVE path separator
 * (verified against Bun 1.3.14 source), so on Windows every returned path
 * is backslash-joined. RULES, exclude basenames, and the report are all
 * posix-relative, so normalize at the discovery boundary before anything
 * matches against a path.
 */
export function toPosixRel(raw, separator = sep) {
  return raw.split(separator).join('/')
}

/**
 * Expands RULES against `root` into one entry per in-scope JSON file.
 * Order is deterministic (include order, then path) so reports and tests
 * are stable. A file matched by two rules keeps the FIRST rule -- the
 * earlier rule is the more specific scope by convention.
 *
 * `*.schema.json` files are schema, never data: the exclusion is applied
 * here once, not per rule, so a new rule cannot forget it and report the
 * directory's own schemas as schemaless data.
 */
const SCHEMA_FILE = new Bun.Glob('*.schema.json')

export function discoverJsonFiles(root) {
  const seen = new Set()
  const files = []
  for (const rule of RULES) {
    const glob = new Bun.Glob(rule.include)
    for (const raw of glob.scanSync({ cwd: root, onlyFiles: true, dot: false })) {
      const rel = toPosixRel(raw)
      if (SCHEMA_FILE.match(posix.basename(rel)))
        continue
      if (seen.has(rel))
        continue
      if ((rule.exclude ?? []).some(ex => new Bun.Glob(ex).match(posix.basename(rel))))
        continue
      seen.add(rel)
      files.push({ file: rel, rule })
    }
  }
  files.sort((a, b) => a.file.localeCompare(b.file))
  return files
}

/**
 * The schema path for `entry`, root-joined, or null when neither the rule
 * nor a sibling names one that exists. A null here is a failure the caller
 * reports -- never a skip.
 */
export function resolveSchemaPath(entry, root = '.') {
  if (entry.rule.schema) {
    const p = posix.join(root, entry.rule.schema)
    return existsSync(p) ? p : null
  }
  const base = posix.join(root, entry.file.replace(/\.json$/, '.schema.json'))
  return existsSync(base) ? base : null
}

/**
 * Validates the data file at `dataPath` against the schema file at
 * `schemaPath`, reusing `compiled` as the compile cache. Returns a failure
 * record, or null when the data conforms. Never throws: unreadable or
 * unparseable files on either side of the check are failures, not crashes.
 * `reason` is 'invalid' (with `errors`) or 'bad-schema' (with `reasonText`).
 */
export function validateAgainstSchema(ajv, compiled, dataPath, schemaPath) {
  let validate
  if (compiled.has(schemaPath)) {
    validate = compiled.get(schemaPath)
  }
  else {
    let schema
    try {
      schema = JSON.parse(readFileSync(schemaPath, 'utf8'))
    }
    catch (err) {
      return { reason: 'bad-schema', reasonText: `schema is not valid JSON: ${err.message}` }
    }
    validate = ajv.compile(schema)
    compiled.set(schemaPath, validate)
  }
  let data
  try {
    data = JSON.parse(readFileSync(dataPath, 'utf8'))
  }
  catch (err) {
    return { reason: 'invalid', errors: [{ path: '/', message: `not valid JSON: ${err.message}` }] }
  }
  if (!validate(data)) {
    return {
      reason: 'invalid',
      errors: (validate.errors ?? []).map(e => ({
        path: e.instancePath || '/',
        message: `${e.message ?? 'invalid'}${e.params ? ` (${JSON.stringify(e.params)})` : ''}`,
      })),
    }
  }
  return null
}

/**
 * Validates every discovered file. Returns a report:
 *   { files: n, failures: [{ file, reason, errors?/reasonText? }] }
 * `reason` is 'no-schema', 'bad-schema', or 'invalid'. Never throws for bad
 * data or a bad schema.
 */
export function validateAll(root, { ajv = buildAjv() } = {}) {
  const failures = []
  const compiled = new Map()
  const entries = discoverJsonFiles(root)
  for (const entry of entries) {
    const schemaPath = resolveSchemaPath(entry, root)
    if (!schemaPath) {
      failures.push({
        file: entry.file,
        reason: 'no-schema',
        reasonText: `no schema: neither rule nor sibling <name>.schema.json covers it`,
      })
      continue
    }
    const failure = validateAgainstSchema(ajv, compiled, posix.join(root, entry.file), schemaPath)
    if (failure)
      failures.push({ file: entry.file, ...failure })
  }
  return { files: entries.length, failures }
}

/**
 * Validates every `*.json` (except `*.schema.json`) directly under `dir`
 * against its sibling schema. Returns the same failure shape as validateAll.
 * This is the entry point the contracts generator uses: generation must fail
 * on an invalid contract BEFORE any output is written, not at lint time.
 */
export function validateSchemalessDir(dir, { ajv = buildAjv() } = {}) {
  const failures = []
  const compiled = new Map()
  const names = Array.from(new Bun.Glob('*.json').scanSync({ cwd: dir, onlyFiles: true }))
    .map(name => toPosixRel(name))
    .filter(n => !n.endsWith('.schema.json'))
    .sort()
  for (const name of names) {
    const schemaPath = posix.join(dir, name.replace(/\.json$/, '.schema.json'))
    if (!existsSync(schemaPath)) {
      failures.push({ file: name, reason: 'no-schema', reasonText: 'no sibling <name>.schema.json' })
      continue
    }
    const failure = validateAgainstSchema(ajv, compiled, posix.join(dir, name), schemaPath)
    if (failure)
      failures.push({ file: name, ...failure })
  }
  return { files: names.length, failures }
}

/**
 * Renders a failures report (from validateAll/validateSchemalessDir) as one
 * line per error. Shared by this script's CLI and generate-contracts, so a
 * failed contract reads identically from `task validate-json` and `task
 * generate-contracts`. `filePrefix` rewrites each file path (the generator
 * reports `contracts/<name>.json`).
 */
export function formatFailureLines(failures, filePrefix = '') {
  const lines = []
  for (const f of failures) {
    if (f.reason === 'invalid') {
      for (const e of f.errors)
        lines.push(`${filePrefix}${f.file}${e.path}: ${e.message}`)
    }
    else {
      lines.push(`${filePrefix}${f.file}: ${f.reasonText}`)
    }
  }
  return lines
}

// `root` is the working directory for discovery; paths inside the report are
// repo-relative so CI output reads the same locally and on a runner.
if (import.meta.main) {
  const root = argv[2] ?? '.'
  const { files, failures } = validateAll(root)
  if (failures.length > 0) {
    console.error(`validate-json: ${failures.length} of ${files} files failed`)
    for (const line of formatFailureLines(failures))
      console.error(`  ${line}`)
    console.error(`Add a sibling <name>.schema.json (or extend RULES in scripts/validate-json.mjs), fix the file, then re-run \`task validate-json\`.`)
    exit(1)
  }
  console.log(`validate-json: ${files} files valid against their schemas`)
}
