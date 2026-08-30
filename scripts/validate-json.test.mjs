// Tests for validate-json.mjs, run by `bun test` via `task test-scripts`.
//
// The discovery and resolution logic is what carries the risk: the
// no-schemaless-JSON rule only holds while every in-scope file actually
// resolves to a schema, and while a rule's include/exclude pair cannot
// silently stop matching the files it was written for. These tests pin both
// against the REAL repo tree (not a fixture copy), the same way
// sync-versions.test.mjs pins its claim registry.

import { mkdirSync, mkdtempSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { describe, expect, it } from 'bun:test'

import { buildAjv, discoverJsonFiles, formatFailureLines, resolveSchemaPath, RULES, toPosixRel, validateAll, validateSchemalessDir } from './validate-json.mjs'

const ROOT = resolve(import.meta.dirname, '..')

describe('RULES', () => {
  it('keeps every schema file itself out of scope', () => {
    // The sibling convention would otherwise pull *.schema.json files in as
    // data files needing their own schemas, forever.
    const inScope = discoverJsonFiles(ROOT).map(e => e.file)
    for (const f of inScope)
      expect(f.endsWith('.schema.json')).toBe(false)
  })

  it('orders specific rules before the generic testdata glob', () => {
    // The two CRDT corpora share one schema via a rule that must win over
    // the generic sibling-resolution rule for testdata/*.json. If the generic
    // rule ever moves up, those two files start demanding siblings that do
    // not exist and the suite goes red for a routing reason, not a data one.
    const sharedIdx = RULES.findIndex(r => r.schema === 'testdata/crdt_projection.schema.json')
    const genericIdx = RULES.findIndex(r => r.include === 'testdata/*.json')
    expect(sharedIdx).toBeGreaterThanOrEqual(0)
    expect(genericIdx).toBeGreaterThan(sharedIdx)
  })
})

describe('discoverJsonFiles', () => {
  it('covers the shipped fixtures and nothing from node_modules or generated trees', () => {
    const files = discoverJsonFiles(ROOT).map(e => e.file)
    for (const must of [
      'testdata/crdt_projection_conformance.json',
      'testdata/crdt_projection_corpus.json',
      'testdata/noise_rekey_vectors.json',
      'backend/internal/hub/usersettings/testdata/account_schema.json',
      'frontend/src/lib/syntaxThemes/nord-light.json',
      'scripts/license-overrides/extra/pi-mono/metadata.json',
      'scripts/license-overrides/go/github.com-bmizerany-assert/expected.json',
    ]) {
      expect(files).toContain(must)
    }
    for (const f of files) {
      expect(f.includes('node_modules')).toBe(false)
      expect(f.includes('/generated/')).toBe(false)
      // The gitignored spinner OUTPUT tree. The license metadata ABOUT the
      // spinner assets (scripts/license-overrides/extra/awesome-claude-
      // spinners/) is committed and must stay in scope.
      expect(f.startsWith('frontend/src/spinners')).toBe(false)
    }
  })

  it('dedupes a file matched by two rules, keeping the first rule', () => {
    const files = discoverJsonFiles(ROOT)
    const names = files.map(e => e.file)
    expect(new Set(names).size).toBe(names.length)
    const corpus = files.find(e => e.file === 'testdata/crdt_projection_corpus.json')
    expect(corpus?.rule.schema).toBe('testdata/crdt_projection.schema.json')
  })

  it('normalizes the native separator scanSync emits on Windows', () => {
    // Bun's scanSync joins results with the OS separator, so a Windows run
    // hands back backslash-joined paths. Without normalization the exclude
    // glob matches the WHOLE path instead of the basename and every
    // *.schema.json enters scope as data. The separator is injectable so
    // this pins the Windows behavior from any OS.
    expect(toPosixRel('contracts\\wire.schema.json', '\\')).toBe('contracts/wire.schema.json')
    expect(toPosixRel('contracts/wire.json', '/')).toBe('contracts/wire.json')
  })
})

describe('resolveSchemaPath', () => {
  it('prefers the rule schema, falling back to the sibling convention', () => {
    const shared = resolveSchemaPath({
      file: 'testdata/hlc_wire_corpus.json',
      rule: { include: 'testdata/*.json' },
    })
    expect(shared).toBe('testdata/hlc_wire_corpus.schema.json')
    const explicit = resolveSchemaPath({
      file: 'testdata/crdt_projection_corpus.json',
      rule: { include: 'x', schema: 'testdata/crdt_projection.schema.json' },
    })
    expect(explicit).toBe('testdata/crdt_projection.schema.json')
  })

  it('returns null when no sibling exists', () => {
    expect(resolveSchemaPath({
      file: 'testdata/does-not-exist.json',
      rule: { include: 'testdata/*.json' },
    })).toBeNull()
  })
})

describe('validateAll', () => {
  it('passes on the real repo tree', () => {
    const { failures } = validateAll(ROOT)
    expect(failures).toEqual([])
  })

  it('ignores a JSON file no include pattern matches', () => {
    const dir = mkdtempSync(join(tmpdir(), 'validate-json-'))
    // At the temp root, not under contracts/ or testdata/: out of scope by
    // design, so it is neither validated nor reported as schemaless.
    writeFileSync(join(dir, 'wire.json'), '{}')
    const { failures } = validateAll(dir, { ajv: buildAjv() })
    expect(failures).toEqual([])
  })

  it('reports invalid data and schemaless files separately', () => {
    const dir = mkdtempSync(join(tmpdir(), 'validate-json-'))
    mkdirSync(join(dir, 'testdata'))
    mkdirSync(join(dir, 'contracts'))
    writeFileSync(join(dir, 'contracts', 'retry.json'), JSON.stringify({ nope: true }))
    writeFileSync(join(dir, 'contracts', 'retry.schema.json'), JSON.stringify({
      type: 'object',
      additionalProperties: false,
      required: ['policies'],
      properties: { policies: { type: 'object' } },
    }))
    writeFileSync(join(dir, 'testdata', 'lonely.json'), '[]')
    const { failures } = validateAll(dir, { ajv: buildAjv() })
    const reasons = Object.fromEntries(failures.map(f => [f.file, f.reason]))
    expect(reasons['contracts/retry.json']).toBe('invalid')
    expect(reasons['testdata/lonely.json']).toBe('no-schema')
    const invalid = failures.find(f => f.reason === 'invalid')
    expect(invalid?.errors[0]?.path).toBe('/')
  })

  it('reports an unparseable data file and an unparseable schema as failures, not crashes', () => {
    const dir = mkdtempSync(join(tmpdir(), 'validate-json-'))
    mkdirSync(join(dir, 'contracts'))
    writeFileSync(join(dir, 'contracts', 'broken.json'), '{not json')
    writeFileSync(join(dir, 'contracts', 'broken.schema.json'), '{also not json')
    writeFileSync(join(dir, 'contracts', 'fine.json'), '{"fine":true}')
    writeFileSync(join(dir, 'contracts', 'fine.schema.json'), JSON.stringify({
      type: 'object',
      additionalProperties: false,
    }))
    const { failures } = validateAll(dir, { ajv: buildAjv() })
    const reasons = Object.fromEntries(failures.map(f => [f.file, f.reason]))
    expect(reasons['contracts/broken.json']).toBe('bad-schema')
    expect(reasons['contracts/fine.json']).toBe('invalid')
  })
})

describe('formatFailureLines', () => {
  it('renders each invalid error with its instance path and every other reason as one line', () => {
    const lines = formatFailureLines([
      { file: 'a.json', reason: 'invalid', errors: [{ path: '/policies', message: 'must be object' }, { path: '/', message: 'bad' }] },
      { file: 'b.json', reason: 'no-schema', reasonText: 'no sibling schema' },
    ])
    expect(lines).toEqual([
      'a.json/policies: must be object',
      'a.json/: bad',
      'b.json: no sibling schema',
    ])
  })

  it('prefixes each file path so both CLIs report one failure identically', () => {
    // generate-contracts reports contracts/<name>.json; the prefix must not
    // leak into the message text or the reason ordering.
    const lines = formatFailureLines([{ file: 'wire.json', reason: 'no-schema', reasonText: 'x' }], 'contracts/')
    expect(lines).toEqual(['contracts/wire.json: x'])
  })
})

describe('validateSchemalessDir', () => {
  it('reports a schemaless and an invalid contract separately', () => {
    const dir = mkdtempSync(join(tmpdir(), 'validate-json-schemaless-'))
    writeFileSync(join(dir, 'schemaless.json'), '{}')
    writeFileSync(join(dir, 'invalid.json'), '{"nope":1}')
    writeFileSync(join(dir, 'invalid.schema.json'), JSON.stringify({
      type: 'object',
      additionalProperties: false,
    }))
    const { files, failures } = validateSchemalessDir(dir, { ajv: buildAjv() })
    expect(files).toBe(2)
    const reasons = Object.fromEntries(failures.map(f => [f.file, f.reason]))
    expect(reasons['schemaless.json']).toBe('no-schema')
    expect(reasons['invalid.json']).toBe('invalid')
  })

  it('keeps a digit in the file name intact', () => {
    // `names.map(toPosixRel)` passes the array index as toPosixRel's
    // `separator` parameter, and `split(0)` cuts the name at every "0": a
    // contract named v0.json was reported against a bogus v/.json path
    // while the real file silently never validated. The map call must pass
    // the name alone.
    const dir = mkdtempSync(join(tmpdir(), 'validate-json-digit-'))
    writeFileSync(join(dir, 'v0.json'), '{"ok":true}')
    writeFileSync(join(dir, 'v0.schema.json'), JSON.stringify({
      type: 'object',
      additionalProperties: false,
      required: ['ok'],
      properties: { ok: { type: 'boolean' } },
    }))
    const { files, failures } = validateSchemalessDir(dir, { ajv: buildAjv() })
    expect(files).toBe(1)
    expect(failures).toEqual([])
  })
})
