// Validate project-written JSON against JSON Schema draft 2020-12.
// Run this script through `task validate-json`.
// Each selected file requires the rule's schema or a sibling <name>.schema.json.
// A selected file without a schema fails validation.
// A key that one object states more than once fails validation also, in a data file
// and in a schema (see findDuplicateKeys).
// External tools define their own package, configuration, and lock-file formats.
// RULES excludes those formats and gitignored build output.
// createRequire resolves Ajv from frontend/node_modules because the repository has no root package.json.

import { existsSync, readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { posix, sep } from 'node:path'
import { argv, exit } from 'node:process'

/** One scope of JSON files plus how its schema is found. */
export const RULES = [
  // Each cross-language contract requires a sibling schema.
  { include: 'contracts/*.json' },
  // Both language suites use these conformance fixtures.
  // The two CRDT corpora share one schema because they have the same shape.
  // One corpus is maintained manually. A generator writes the other.
  {
    include: 'testdata/crdt_projection_{conformance,corpus}.json',
    schema: 'testdata/crdt_projection.schema.json',
  },
  { include: 'testdata/*.json' },
  // Backend packages keep their local test fixtures beside their tests.
  { include: 'backend/**/testdata/*.json' },
  // The published provider matrix and its feature definitions are E2E data.
  { include: 'frontend/tests/e2e/feature-matrix/*.json' },
  // Vendored Shiki and VS Code themes share one schema.
  {
    include: 'frontend/src/lib/syntaxThemes/*.json',
    schema: 'frontend/src/lib/syntaxThemes/syntax-theme.schema.json',
  },
  // NOTICE metadata uses one shared schema for each file name.
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

/**
 * Create a strict validator for the repository's schema draft.
 */
export function buildAjv() {
  return new Ajv2020({ strict: true, allErrors: true })
}

/**
 * Bun's Glob.scanSync uses the native path separator, as Bun 1.3.14 source confirms.
 * Windows paths therefore contain backslashes.
 * RULES and report paths use POSIX separators.
 * Normalize each discovered path before matching it.
 */
export function toPosixRel(raw, separator = sep) {
  return raw.split(separator).join('/')
}

/**
 * Expand RULES into one entry for each selected JSON file under root.
 * The first matching rule supplies the schema. Put more specific rules first.
 * Sort file paths to keep reports and tests deterministic.
 * Exclude schema files here so every rule treats them as schemas instead of data.
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
 * Resolve the rule's schema or the sibling schema under root.
 * Return null when neither schema exists. The caller must report that failure.
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
 * List each key that one object of a JSON text states more than once.
 *
 * JSON.parse keeps the last value of a repeated key and reports nothing. A merge that
 * repeats a key therefore changes the data without notice, and no schema can see the
 * value that JSON.parse dropped. Call this only on text that JSON.parse accepted: the
 * scan reads the tokens and does not check the grammar again.
 *
 * The scan compares the decoded keys, so "a" and "\u0061" are one key. It walks the
 * text without recursion, so no nesting depth can overflow the stack, and it builds a
 * pointer only for a key that it reports.
 *
 * Return one { path, key } for each extra occurrence. path is the JSON Pointer of the
 * object that states the key, and '' is the root.
 */
export function findDuplicateKeys(text) {
  const duplicates = []
  // One frame for each open object or array. Its segment is the pointer step from its
  // parent. An object frame holds its keys, the key of its current member, and whether
  // the next string is a key. An array frame counts its elements through the commas.
  const stack = []
  const nextSegment = () => {
    const parent = stack.at(-1)
    if (!parent)
      return ''
    return parent.keys ? `/${escapePointerToken(parent.key)}` : `/${parent.index}`
  }
  for (let i = 0; i < text.length; i++) {
    const c = text[i]
    if (c === '"') {
      let end = i + 1
      while (end < text.length && text[end] !== '"')
        end += text[end] === '\\' ? 2 : 1
      const top = stack.at(-1)
      if (top?.keys && top.expectKey) {
        const key = JSON.parse(text.slice(i, end + 1))
        if (top.keys.has(key))
          duplicates.push({ path: stack.map(frame => frame.segment).join(''), key })
        top.keys.add(key)
        top.key = key
        top.expectKey = false
      }
      i = end
    }
    else if (c === '{') {
      stack.push({ segment: nextSegment(), keys: new Set(), key: '', expectKey: true })
    }
    else if (c === '[') {
      stack.push({ segment: nextSegment(), keys: null, index: 0 })
    }
    else if (c === '}' || c === ']') {
      stack.pop()
    }
    else if (c === ',') {
      const top = stack.at(-1)
      if (top?.keys)
        top.expectKey = true
      else if (top)
        top.index++
    }
  }
  return duplicates
}

/** Escape one JSON Pointer token (RFC 6901): `~` becomes `~0`, and `/` becomes `~1`. */
function escapePointerToken(token) {
  return token.replaceAll('~', '~0').replaceAll('/', '~1')
}

/** The message for one repeated key in a data file. */
function duplicateKeyMessage(key) {
  return `the key ${JSON.stringify(key)} appears more than once in this object. JSON.parse keeps only the last value.`
}

/**
 * Validate dataPath against schemaPath and reuse successfully compiled schemas.
 * Return null when the data conforms. Return a failure record otherwise.
 * File read, JSON parse, and schema compilation failures return records instead of exceptions.
 * Invalid data uses reason 'invalid' with errors. Schema failures use 'bad-schema' with reasonText.
 * A repeated key makes the data invalid, or the schema bad, although JSON.parse accepts it.
 * The errors of a data file list the repeated keys first, then the schema errors.
 */
export function validateAgainstSchema(ajv, compiled, dataPath, schemaPath) {
  let validate
  if (compiled.has(schemaPath)) {
    validate = compiled.get(schemaPath)
  }
  else {
    let schema
    let schemaText
    try {
      schemaText = readFileSync(schemaPath, 'utf8')
      schema = JSON.parse(schemaText)
    }
    catch (err) {
      return { reason: 'bad-schema', reasonText: `schema is not valid JSON: ${err.message}` }
    }
    const schemaDuplicates = findDuplicateKeys(schemaText)
    if (schemaDuplicates.length > 0) {
      const list = schemaDuplicates.map(d => `the key ${JSON.stringify(d.key)} at ${d.path || '/'}`).join(', ')
      return { reason: 'bad-schema', reasonText: `schema states a key more than once: ${list}. JSON.parse keeps only the last value.` }
    }
    try {
      validate = ajv.compile(schema)
    }
    catch (err) {
      const detail = err instanceof Error ? err.message : String(err)
      return { reason: 'bad-schema', reasonText: `schema cannot compile: ${detail}` }
    }
    compiled.set(schemaPath, validate)
  }
  let data
  let dataText
  try {
    dataText = readFileSync(dataPath, 'utf8')
    data = JSON.parse(dataText)
  }
  catch (err) {
    return { reason: 'invalid', errors: [{ path: '/', message: `not valid JSON: ${err.message}` }] }
  }
  const errors = findDuplicateKeys(dataText).map(d => ({ path: d.path || '/', message: duplicateKeyMessage(d.key) }))
  if (!validate(data)) {
    for (const e of validate.errors ?? []) {
      errors.push({
        path: e.instancePath || '/',
        message: `${e.message ?? 'invalid'}${e.params ? ` (${JSON.stringify(e.params)})` : ''}`,
      })
    }
  }
  return errors.length > 0 ? { reason: 'invalid', errors } : null
}

/**
 * Validate every discovered file. Return this report:
 * { files: n, failures: [{ file, reason, errors?/reasonText? }] }
 * Each failure uses reason 'no-schema', 'bad-schema', or 'invalid'.
 * Bad data and bad schemas return failure records instead of exceptions.
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
 * Validate each JSON data file directly under dir against its sibling schema.
 * Exclude schema files. Return the same failure shape as validateAll.
 * The contracts generator calls this function before writing output.
 * An invalid contract must stop generation at that point.
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
 * Render one line for each failure from validateAll or validateSchemalessDir.
 * The CLI and contracts generator share this function to report the same errors.
 * filePrefix adds the generator's contracts/ prefix to each file path.
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

// root supplies the discovery directory.
// Report paths stay relative to the repository for consistent local and CI output.
if (import.meta.main) {
  const root = argv[2] ?? '.'
  const { files, failures } = validateAll(root)
  if (failures.length > 0) {
    console.error(`validate-json: ${failures.length} of ${files} files failed`)
    for (const line of formatFailureLines(failures))
      console.error(`  ${line}`)
    console.error('Add a sibling <name>.schema.json or extend RULES in scripts/validate-json.mjs.')
    console.error('Correct the reported file.')
    console.error('Run task validate-json again.')
    exit(1)
  }
  console.log(`validate-json: ${files} files valid against their schemas`)
}
