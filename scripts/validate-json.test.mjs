// Test the JSON validator through `task test-scripts`.
// Discovery must select every project-owned fixture that requires a schema.
// Resolution must report a missing schema instead of silently excluding a file.
// Tests inspect the real repository tree to catch changes to rule patterns.

import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { afterEach, describe, expect, it } from 'bun:test'

import { buildAjv, discoverJsonFiles, findDuplicateKeys, formatFailureLines, resolveSchemaPath, RULES, toPosixRel, validateAll, validateSchemalessDir } from './validate-json.mjs'

const ROOT = resolve(import.meta.dirname, '..')
const SCRATCH_ROOT = join(ROOT, '.tmp')
const scratchDirs = []

function scratchDirectory(prefix) {
  mkdirSync(SCRATCH_ROOT, { recursive: true })
  const directory = mkdtempSync(join(SCRATCH_ROOT, prefix))
  scratchDirs.push(directory)
  return directory
}

afterEach(() => {
  for (const directory of scratchDirs.splice(0))
    rmSync(directory, { recursive: true, force: true })
})

describe('RULES', () => {
  it('keeps every schema file itself out of scope', () => {
    // Schema files must not require another sibling schema.
    const inScope = discoverJsonFiles(ROOT).map(e => e.file)
    for (const f of inScope)
      expect(f.endsWith('.schema.json')).toBe(false)
  })

  it('orders specific rules before the generic testdata glob', () => {
    // The two CRDT corpora use one explicit shared schema.
    // Their specific rule must precede the generic sibling rule.
    // Otherwise validation requires absent sibling schemas for both corpora.
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
      'frontend/tests/e2e/feature-matrix/features.json',
      'frontend/tests/e2e/feature-matrix/checklist.json',
      'scripts/license-overrides/extra/pi-mono/metadata.json',
      'scripts/license-overrides/go/github.com-altcha-org-altcha-lib-go-v2/expected.json',
    ]) {
      expect(files).toContain(must)
    }
    for (const f of files) {
      expect(f.includes('node_modules')).toBe(false)
      expect(f.includes('/generated/')).toBe(false)
      // Exclude generated spinner output.
      // Keep committed spinner license metadata inside the validation scope.
      expect(f.startsWith('frontend/src/spinners')).toBe(false)
    }
  })

  it('deduplicates a file matched by two rules and keeps the first rule', () => {
    const files = discoverJsonFiles(ROOT)
    const names = files.map(e => e.file)
    expect(new Set(names).size).toBe(names.length)
    const corpus = files.find(e => e.file === 'testdata/crdt_projection_corpus.json')
    expect(corpus?.rule.schema).toBe('testdata/crdt_projection.schema.json')
  })

  it('normalizes the native separator scanSync emits on Windows', () => {
    // Bun's scanSync uses the OS path separator.
    // Normalize Windows paths before matching each schema basename.
    // Otherwise schema files enter the data scope.
    // The explicit separator tests Windows paths on every OS.
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
    const dir = scratchDirectory('validate-json-')
    // A JSON file at this isolated root matches neither contracts/ nor testdata/.
    // The validator must exclude it.
    writeFileSync(join(dir, 'wire.json'), '{}')
    const { failures } = validateAll(dir, {
      ajv: buildAjv(),
    })
    expect(failures).toEqual([])
  })

  it('reports invalid data and schemaless files separately', () => {
    const dir = scratchDirectory('validate-json-')
    mkdirSync(join(dir, 'testdata'))
    mkdirSync(join(dir, 'contracts'))
    writeFileSync(join(dir, 'contracts', 'retry.json'), JSON.stringify({ nope: true }))
    writeFileSync(join(dir, 'contracts', 'retry.schema.json'), JSON.stringify({
      type: 'object',
      additionalProperties: false,
      required: ['policies'],
      properties: {
        policies: { type: 'object' },
      },
    }))
    writeFileSync(join(dir, 'testdata', 'lonely.json'), '[]')
    const { failures } = validateAll(dir, {
      ajv: buildAjv(),
    })
    const reasons = Object.fromEntries(failures.map(f => [f.file, f.reason]))
    expect(reasons['contracts/retry.json']).toBe('invalid')
    expect(reasons['testdata/lonely.json']).toBe('no-schema')
    const invalid = failures.find(f => f.reason === 'invalid')
    expect(invalid?.errors[0]?.path).toBe('/')
  })

  it('reports an unparseable data file and an unparseable schema as failures, not crashes', () => {
    const dir = scratchDirectory('validate-json-')
    mkdirSync(join(dir, 'contracts'))
    writeFileSync(join(dir, 'contracts', 'broken.json'), '{not json')
    writeFileSync(join(dir, 'contracts', 'broken.schema.json'), '{also not json')
    writeFileSync(join(dir, 'contracts', 'fine.json'), '{"fine":true}')
    writeFileSync(join(dir, 'contracts', 'fine.schema.json'), JSON.stringify({
      type: 'object',
      additionalProperties: false,
    }))
    const { failures } = validateAll(dir, {
      ajv: buildAjv(),
    })
    const reasons = Object.fromEntries(failures.map(f => [f.file, f.reason]))
    expect(reasons['contracts/broken.json']).toBe('bad-schema')
    expect(reasons['contracts/fine.json']).toBe('invalid')
  })

  it.each([
    [
      'unsupported draft',
      { $schema: 'http://json-schema.org/draft-07/schema#', type: 'object' },
      'draft-07',
    ],
    [
      'unknown keyword',
      { type: 'object', unsupportedKeyword: true },
      'unsupportedKeyword',
    ],
  ])('reports a schema compilation failure for %s and continues with later files', (_name, schema, detail) => {
    const dir = scratchDirectory('validate-json-compile-')
    mkdirSync(join(dir, 'contracts'))
    writeFileSync(join(dir, 'contracts', 'a-broken.json'), '{}')
    writeFileSync(join(dir, 'contracts', 'a-broken.schema.json'), JSON.stringify(schema))
    writeFileSync(join(dir, 'contracts', 'z-valid.json'), '{"value":0}')
    writeFileSync(join(dir, 'contracts', 'z-valid.schema.json'), JSON.stringify({
      type: 'object',
      additionalProperties: false,
      required: ['value'],
      properties: {
        value: { type: 'integer' },
      },
    }))

    const result = validateAll(dir)

    expect(result.files).toBe(2)
    expect(result.failures).toHaveLength(1)
    expect(result.failures[0]?.file).toBe('contracts/a-broken.json')
    expect(result.failures[0]?.reason).toBe('bad-schema')
    expect(result.failures[0]?.reasonText).toContain(detail)
    expect(formatFailureLines(result.failures)[0]).toContain('contracts/a-broken.json:')
  })
})

describe('findDuplicateKeys', () => {
  it('reports nothing when each object states each key once', () => {
    expect(findDuplicateKeys('{"a":1,"b":{"a":2},"c":[{"a":3},{"a":4}],"d":[]}')).toEqual([])
  })

  it('reports a repeated key with the JSON Pointer of its object', () => {
    expect(findDuplicateKeys('{"methods":{"Warning":"w","Error":"e","Warning":"w"}}')).toEqual([
      { path: '/methods', key: 'Warning' },
    ])
  })

  it('reports a repeated key of the root object with the root pointer', () => {
    expect(findDuplicateKeys('{"a":1,"a":2}')).toEqual([
      { path: '', key: 'a' },
    ])
  })

  it('reports each extra occurrence of a key', () => {
    expect(findDuplicateKeys('{"a":1,"a":2,"a":3}')).toEqual([
      { path: '', key: 'a' },
      { path: '', key: 'a' },
    ])
  })

  it('compares the decoded keys, so an escaped spelling is the same key', () => {
    expect(findDuplicateKeys('{"a":1,"\\u0061":2}')).toEqual([
      { path: '', key: 'a' },
    ])
  })

  it('reads strings that hold JSON punctuation, quotes and backslashes as one token', () => {
    expect(findDuplicateKeys('{"k":"{\\"k\\":[1,2]}","k\\\\":"]","\\"k\\"":",","x":"}"}')).toEqual([])
  })

  it('counts array elements of every type in the pointer', () => {
    expect(findDuplicateKeys('[1,"s",true,null,[],{"b":1,"b":2}]')).toEqual([
      { path: '/5', key: 'b' },
    ])
  })

  it('escapes a tilde and a slash in the pointer', () => {
    expect(findDuplicateKeys('{"a/b~c":{"x":1,"x":2}}')).toEqual([
      { path: '/a~1b~0c', key: 'x' },
    ])
  })

  it('reads a key again after a nested value closes', () => {
    expect(findDuplicateKeys('{"a":{"b":[1,{"c":2}]},"a":3}')).toEqual([
      { path: '', key: 'a' },
    ])
  })

  it('accepts whitespace between every token', () => {
    expect(findDuplicateKeys(' {\n  "a" : 1 ,\n  "a" : [ 1 , { "b" : 2 } ]\n}\n')).toEqual([
      { path: '', key: 'a' },
    ])
  })

  it('walks a deep nesting without recursion', () => {
    const depth = 20000
    const text = `${'['.repeat(depth)}{"a":1,"a":2}${']'.repeat(depth)}`
    expect(findDuplicateKeys(text)).toEqual([
      {
        path: '/0'.repeat(depth),
        key: 'a',
      },
    ])
  })

  it('reports nothing for a top-level scalar', () => {
    expect(findDuplicateKeys('"a"')).toEqual([])
    expect(findDuplicateKeys('42')).toEqual([])
  })
})

describe('validateAll duplicate keys', () => {
  it('reports a repeated key in a data file although the last value conforms', () => {
    const dir = scratchDirectory('validate-json-duplicate-')
    mkdirSync(join(dir, 'contracts'))
    writeFileSync(join(dir, 'contracts', 'dup.json'), '{"value":"text","value":1}')
    writeFileSync(join(dir, 'contracts', 'dup.schema.json'), JSON.stringify({
      type: 'object',
      additionalProperties: false,
      required: ['value'],
      properties: {
        value: { type: 'integer' },
      },
    }))
    const { failures } = validateAll(dir, {
      ajv: buildAjv(),
    })
    expect(failures).toHaveLength(1)
    expect(failures[0]?.reason).toBe('invalid')
    expect(formatFailureLines(failures)).toEqual([
      'contracts/dup.json/: the key "value" appears more than once in this object. JSON.parse keeps only the last value.',
    ])
  })

  it('reports a repeated key beside a schema violation in the same file', () => {
    const dir = scratchDirectory('validate-json-duplicate-')
    mkdirSync(join(dir, 'contracts'))
    writeFileSync(join(dir, 'contracts', 'dup.json'), '{"nested":{"a":1,"a":2},"value":"text"}')
    writeFileSync(join(dir, 'contracts', 'dup.schema.json'), JSON.stringify({
      type: 'object',
      properties: {
        value: { type: 'integer' },
      },
    }))
    const { failures } = validateAll(dir, {
      ajv: buildAjv(),
    })
    const lines = formatFailureLines(failures)
    expect(lines).toHaveLength(2)
    expect(lines[0]).toBe('contracts/dup.json/nested: the key "a" appears more than once in this object. JSON.parse keeps only the last value.')
    expect(lines[1]).toContain('contracts/dup.json/value: must be integer')
  })

  it('reports a repeated key in a schema as a bad schema', () => {
    const dir = scratchDirectory('validate-json-duplicate-')
    mkdirSync(join(dir, 'contracts'))
    writeFileSync(join(dir, 'contracts', 'dup.json'), '[]')
    writeFileSync(join(dir, 'contracts', 'dup.schema.json'), '{"type":"object","type":"array"}')
    const { failures } = validateAll(dir, {
      ajv: buildAjv(),
    })
    expect(failures).toHaveLength(1)
    expect(failures[0]?.reason).toBe('bad-schema')
    expect(failures[0]?.reasonText).toBe('schema states a key more than once: the key "type" at /. JSON.parse keeps only the last value.')
  })

  it('reports a repeated key in a contract that the generator validates', () => {
    const dir = scratchDirectory('validate-json-duplicate-')
    writeFileSync(join(dir, 'dup.json'), '{"a":1,"a":1}')
    writeFileSync(join(dir, 'dup.schema.json'), JSON.stringify({ type: 'object' }))
    const { failures } = validateSchemalessDir(dir, {
      ajv: buildAjv(),
    })
    expect(failures.map(f => f.reason)).toEqual(['invalid'])
  })
})

describe('formatFailureLines', () => {
  it('renders each invalid error with its instance path and every other reason as one line', () => {
    const lines = formatFailureLines([
      {
        file: 'a.json',
        reason: 'invalid',
        errors: [
          { path: '/policies', message: 'must be object' },
          { path: '/', message: 'bad' },
        ],
      },
      { file: 'b.json', reason: 'no-schema', reasonText: 'no sibling schema' },
    ])
    expect(lines).toEqual([
      'a.json/policies: must be object',
      'a.json/: bad',
      'b.json: no sibling schema',
    ])
  })

  it('prefixes each file path so both CLIs report one failure identically', () => {
    // The contracts generator adds contracts/ to each reported file path.
    // Keep the message and reason order unchanged.
    const lines = formatFailureLines([
      { file: 'wire.json', reason: 'no-schema', reasonText: 'x' },
    ], 'contracts/')
    expect(lines).toEqual(['contracts/wire.json: x'])
  })
})

describe('validateSchemalessDir', () => {
  it('reports a schemaless and an invalid contract separately', () => {
    const dir = scratchDirectory('validate-json-schemaless-')
    writeFileSync(join(dir, 'schemaless.json'), '{}')
    writeFileSync(join(dir, 'invalid.json'), '{"nope":1}')
    writeFileSync(join(dir, 'invalid.schema.json'), JSON.stringify({
      type: 'object',
      additionalProperties: false,
    }))
    const { files, failures } = validateSchemalessDir(dir, {
      ajv: buildAjv(),
    })
    expect(files).toBe(2)
    const reasons = Object.fromEntries(failures.map(f => [f.file, f.reason]))
    expect(reasons['schemaless.json']).toBe('no-schema')
    expect(reasons['invalid.json']).toBe('invalid')
  })

  it('keeps a digit in the file name intact', () => {
    // Passing toPosixRel directly to map supplies the index as its separator.
    // split(0) then removes each zero from the file name.
    // The validator must retain v0.json and validate that actual file.
    // The map callback must pass only the file name.
    const dir = scratchDirectory('validate-json-digit-')
    writeFileSync(join(dir, 'v0.json'), '{"ok":true}')
    writeFileSync(join(dir, 'v0.schema.json'), JSON.stringify({
      type: 'object',
      additionalProperties: false,
      required: ['ok'],
      properties: {
        ok: { type: 'boolean' },
      },
    }))
    const { files, failures } = validateSchemalessDir(dir, {
      ajv: buildAjv(),
    })
    expect(files).toBe(1)
    expect(failures).toEqual([])
  })
})
