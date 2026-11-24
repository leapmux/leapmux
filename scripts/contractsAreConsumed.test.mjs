import { existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs'
import { dirname, join, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { afterAll, beforeAll, describe, expect, it } from 'bun:test'
import { PROVIDER_PROTOCOLS, resolveProviderSubsetParent, tableReaders, unreadKeys } from './generate-contracts.mjs'

/**
 * Every generated table must be READ on each side that a contract says reads it, and
 * every KEY of it must reach a reader too.
 *
 * `generate-contracts.mjs` already fails for a contract file no domain registers, and
 * for a table a contract carries that `PROVIDER_PROTOCOLS` does not declare. Neither
 * asks the opposite question: does any code IMPORT what the table emits? A table with
 * no importer is worse than dead weight, because the literal it was extracted from is
 * usually still spelled by hand on the other side of the boundary -- so a rename moves
 * the generated half, leaves the hand-written half on the old word, and the build
 * stays green while a row draws empty.
 *
 * That is the exact failure the contracts rule exists to prevent, so it fails here.
 */

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..')

/**
 * Whether a Go package exists only for tests: `testutil`, or a name that ends in
 * `test` (`agenttest`, `acptest`, `storetest`).
 *
 * This is the rule of `testutil.IsTestSupportPackage`
 * (backend/internal/util/testutil/testsupport.go), which keeps these packages out of
 * the shipped binary. JavaScript cannot call the Go function, so the
 * `isGoTestSupportPackage` test replays the cases of the Go test that pins it.
 */
function isGoTestSupportPackage(name) {
  return name === 'testutil' || name.endsWith('test')
}

/** The Go tool compiles no file under a directory with one of these names. */
function isGoIgnoredDirectory(name) {
  return name === 'testdata' || name.startsWith('.') || name.startsWith('_')
}

/**
 * The suffixes of a browser test, or of a test helper beside the module that it
 * serves: a vitest file, a fixture, a test kit, a shared test double, and a
 * compile-time type check. No bundle ships any of them.
 */
const TS_TEST_HELPER_SUFFIXES = [
  '.test.ts',
  '.test.tsx',
  '.fixtures.ts',
  '.testkit.ts',
  '.test-support.ts',
  '.typecheck.ts',
]

/** The file names of the helpers that the provider tests of the chat pipeline share. */
const TS_TEST_HELPER_NAMES = new Set(['testUtils.ts', 'testUtils.tsx'])

/**
 * Whether one file of a source tree is production code. Only production code counts
 * as a reader.
 *
 * A test is not a reader, and neither is the code that only a test runs. A reflection
 * test that pins a hand-written struct tag to a generated constant mentions the
 * constant, and counting that mention let two tables pass with no production reader
 * at all. A shared test double did the same: `frontend/src/test-support/` imported the
 * control-answer tables, and the browser passed as their reader although no module
 * that ships reads them. A table whose Go reader IS a test says so with `goTagPin`,
 * which this file then holds to the stated file.
 *
 * `path` is relative to its source tree (`GO_SOURCE_TREES`, `TS_SOURCE_TREE`).
 *
 * - Go: a `.go` file that the Go tool compiles into a non-test package. That excludes
 *   a `_test.go` file, a file under a directory that the Go tool ignores
 *   (`isGoIgnoredDirectory`), and a file of a test-support package
 *   (`isGoTestSupportPackage`).
 * - TS: a `.ts` or `.tsx` module of the browser, except a file under a `test-support`
 *   directory and a test helper beside its subject (`TS_TEST_HELPER_SUFFIXES`,
 *   `TS_TEST_HELPER_NAMES`).
 */
function isProductionSource(path) {
  const segments = path.split(/[\\/]/)
  const name = segments.at(-1)
  const directories = segments.slice(0, -1)
  if (name.endsWith('.go')) {
    return !name.endsWith('_test.go')
      && !directories.some(isGoIgnoredDirectory)
      && !isGoTestSupportPackage(directories.at(-1) ?? '')
  }
  return !directories.includes('test-support')
    && !TS_TEST_HELPER_NAMES.has(name)
    && !TS_TEST_HELPER_SUFFIXES.some(suffix => name.endsWith(suffix))
}

/** Every production source file of one tree, minus the generated output. */
function sourceFiles(tree, extensions) {
  const found = []
  const walk = (dir) => {
    for (const entry of readdirSync(dir)) {
      const path = join(dir, entry)
      // `generated` holds the emitted Go and TS. A table citing itself proves nothing.
      if (entry === 'generated' || entry === 'node_modules')
        continue
      if (statSync(path).isDirectory())
        walk(path)
      else if (extensions.some(extension => entry.endsWith(extension)) && isProductionSource(relative(tree, path)))
        found.push(path)
    }
  }
  walk(tree)
  return found
}

/**
 * The source with every comment removed, so a table a doc comment merely discusses
 * does not count as read.
 *
 * The scan tracks `"` and `'` strings, because a `//` inside one opens no comment.
 * It leaves a backtick span alone: that is a Go raw string, which holds struct tags,
 * and a TS template literal, which can hold a real `${SYMBOL.Key}` reference.
 */
function stripComments(text) {
  let out = ''
  let i = 0
  while (i < text.length) {
    const c = text[i]
    if (c === '/' && text[i + 1] === '/') {
      while (i < text.length && text[i] !== '\n')
        i++
      continue
    }
    if (c === '/' && text[i + 1] === '*') {
      i += 2
      while (i < text.length && !(text[i] === '*' && text[i + 1] === '/'))
        i++
      i += 2
      continue
    }
    if (c === '"' || c === '\'') {
      const quote = c
      out += c
      i++
      while (i < text.length && text[i] !== quote && text[i] !== '\n') {
        if (text[i] === '\\' && i + 1 < text.length) {
          out += text[i] + text[i + 1]
          i += 2
          continue
        }
        out += text[i]
        i++
      }
      out += text[i] ?? ''
      i++
      continue
    }
    out += c
    i++
  }
  return out
}

function readAll(files) {
  return files.map(path => ({
    path: relative(root, path),
    text: stripComments(readFileSync(path, 'utf8')),
  }))
}

/**
 * The trees that hold the production code of each side. Go imports the generated
 * package from the backend module and from the module of the desktop sidecar. The
 * browser is `frontend/src` alone: the E2E suite under `frontend/tests/` is a test,
 * and the build and lint tooling elsewhere under `frontend/` ships in no bundle.
 */
const GO_SOURCE_TREES = ['backend', 'desktop/go']
const TS_SOURCE_TREE = 'frontend/src'

const goSources = readAll(GO_SOURCE_TREES.flatMap(tree => sourceFiles(join(root, tree), ['.go'])))
const tsSources = readAll(sourceFiles(join(root, TS_SOURCE_TREE), ['.ts', '.tsx']))

describe('sourceFiles', () => {
  let checkout

  /** The files that one tree of the fixture checkout yields, relative to that tree. */
  function collected(tree, extensions) {
    const base = join(checkout, tree)
    return sourceFiles(base, extensions).map(path => relative(base, path).replaceAll('\\', '/')).sort()
  }

  beforeAll(() => {
    const scratch = join(root, '.tmp')
    mkdirSync(scratch, { recursive: true })
    checkout = mkdtempSync(join(scratch, 'contracts-readers-'))
    for (const path of [
      'frontend/src/lib/reader.ts',
      'frontend/src/lib/latest.ts',
      'frontend/src/lib/agenttest/index.ts',
      'frontend/src/components/View.tsx',
      'frontend/src/lib/reader.test.ts',
      'frontend/src/components/View.test.tsx',
      'frontend/src/test-support/controlRequests.ts',
      'frontend/src/test-support/lintFixtures/probe.ts',
      'frontend/src/components/chat/providers/probe/toolResults.fixtures.ts',
      'frontend/src/components/chat/providers/probe/testUtils.tsx',
      'frontend/src/components/chat/providers/testUtils.ts',
      'frontend/src/components/chat/useChatScroll.testkit.ts',
      'frontend/src/lib/channel.test-support.ts',
      'frontend/src/lib/messageSpan.typecheck.ts',
      'frontend/src/generated/contracts/control-answer.ts',
      'frontend/src/lib/notes.md',
      'backend/main.go',
      'backend/internal/worker/agent/agent.go',
      'backend/internal/worker/agent/agent_test.go',
      'backend/internal/worker/agent/testingx/helper.go',
      'backend/internal/worker/agent/agenttest/fake.go',
      'backend/internal/worker/agent/providers/acp/acptest/conformance.go',
      'backend/internal/util/testutil/repo.go',
      'backend/internal/worker/agent/testdata/sample.go',
      'backend/internal/worker/agent/_scratch/draft.go',
      'backend/internal/worker/agent/.hidden/draft.go',
      'backend/generated/contracts/control-answer.go',
    ]) {
      mkdirSync(dirname(join(checkout, path)), { recursive: true })
      writeFileSync(join(checkout, path), '')
    }
  })

  afterAll(() => {
    if (checkout !== undefined)
      rmSync(checkout, { recursive: true, force: true })
  })

  it('collects only the production modules of the browser tree', () => {
    expect(collected('frontend/src', ['.ts', '.tsx'])).toEqual([
      'components/View.tsx',
      'lib/agenttest/index.ts',
      'lib/latest.ts',
      'lib/reader.ts',
    ])
  })

  it('collects only the production packages of a Go tree', () => {
    expect(collected('backend', ['.go'])).toEqual([
      'internal/worker/agent/agent.go',
      'internal/worker/agent/testingx/helper.go',
      'main.go',
    ])
  })
})

describe('isGoTestSupportPackage', () => {
  // `TestIsTestSupport` in backend/cmd/leapmux/no_test_deps_test.go pins the Go rule
  // with cases of the form `thisModule + "<package path>": <want>`. Replaying the same
  // cases here fails this copy of the rule when the Go rule changes.
  it('agrees with each module case of the Go test that pins the rule', () => {
    const text = readFileSync(join(root, 'backend/cmd/leapmux/no_test_deps_test.go'), 'utf8')
    const cases = Array.from(text.matchAll(/thisModule \+ "([^"]+)":\s*(true|false),/g), ([, packagePath, want]) => ({ packagePath, want: want === 'true' }))
    expect(cases.some(({ want }) => want)).toBe(true)
    expect(cases.some(({ want }) => !want)).toBe(true)
    for (const { packagePath, want } of cases)
      expect(isGoTestSupportPackage(packagePath.split('/').at(-1)), packagePath).toBe(want)
  })
})

/** The browser packages that only a test imports. */
const TS_TEST_TOOLING = /^(?:vitest(?:\/.+)?|@vitest\/.+|@solidjs\/testing-library|@testing-library\/.+|@playwright\/test)$/

/** Every module specifier that a TS source imports, re-exports or loads. */
function tsImportSpecifiers(text) {
  return Array.from(text.matchAll(/\b(?:from|import)\s*(?:\(\s*)?['"]([^'"]+)['"]/g), match => match[1])
}

/** Whether a TS module specifier names test tooling or a module that is not production. */
function isTsTestImport(specifier) {
  return TS_TEST_TOOLING.test(specifier) || !isProductionSource(`${specifier.replace(/\.[cm]?[jt]sx?$/, '')}.ts`)
}

/** Every package path that a Go source imports, in a group or alone. */
function goImportPaths(text) {
  const grouped = Array.from(text.matchAll(/\bimport\s*\(([^)]*)\)/g), ([, block]) => Array.from(block.matchAll(/"([^"]+)"/g), match => match[1]))
  const single = Array.from(text.matchAll(/\bimport\s+(?:[\w.]+\s+)?"([^"]+)"/g), match => match[1])
  return [
    ...grouped.flat(),
    ...single,
  ]
}

/** Whether a Go import path is the test framework, testify, or a LeapMux test-support package. */
function isGoTestImport(importPath) {
  return importPath === 'testing' || importPath.startsWith('testing/')
    || importPath === 'github.com/stretchr/testify' || importPath.startsWith('github.com/stretchr/testify/')
    || (importPath.startsWith('github.com/leapmux/') && isGoTestSupportPackage(importPath.split('/').at(-1)))
}

/**
 * The reader rule reads NAMES, so a test helper with a new kind of name would count
 * as a reader again. A helper that imports test tooling or another test helper is
 * test code whatever its name, and production code imports neither, so each sweep
 * below finds such a helper that the rule missed. A data fixture that imports neither
 * still depends on its name alone.
 */
describe('a production source', () => {
  it('finds each form of a browser test import', () => {
    const text = [
      'import { vi } from \'vitest\'',
      'import type { Probe } from \'~/test-support/probe\'',
      'export { probeRows } from \'./toolResults.fixtures\'',
      'const view = await import(\'./View.test\')',
      'import { render } from \'@solidjs/testing-library\'',
      'import { renderProbe } from \'../testUtils\'',
      'import { kit } from \'./useChatScroll.testkit.js\'',
      'import \'./styles.css\'',
      'import { latest } from \'~/lib/latest\'',
    ].join('\n')
    expect(tsImportSpecifiers(text).filter(isTsTestImport)).toEqual([
      'vitest',
      '~/test-support/probe',
      './toolResults.fixtures',
      './View.test',
      '@solidjs/testing-library',
      '../testUtils',
      './useChatScroll.testkit.js',
    ])
  })

  it('finds each form of a Go test import', () => {
    const text = [
      'package probe',
      'import (',
      '\t"fmt"',
      '\t"testing"',
      '\tassert "github.com/stretchr/testify/assert"',
      '\t"github.com/leapmux/leapmux/internal/worker/agent/agenttest"',
      '\t"github.com/leapmux/leapmux/internal/worker/agent"',
      ')',
      'import _ "testing/fstest"',
      'import "github.com/leapmux/leapmux/internal/util/testutil"',
    ].join('\n')
    expect(goImportPaths(text).filter(isGoTestImport)).toEqual([
      'testing',
      'github.com/stretchr/testify/assert',
      'github.com/leapmux/leapmux/internal/worker/agent/agenttest',
      'testing/fstest',
      'github.com/leapmux/leapmux/internal/util/testutil',
    ])
  })

  it('imports no browser test tooling or test helper', () => {
    expect(tsSources.length).toBeGreaterThan(500)
    const offenders = tsSources.flatMap(source => tsImportSpecifiers(source.text)
      .filter(isTsTestImport)
      .map(specifier => `${source.path} imports ${specifier}`))
    expect(offenders, 'a module that the scan counts as production imports test code: give a test helper a name that isProductionSource excludes, or stop importing test code from production').toEqual([])
  })

  it('imports no Go test framework or test-support package', () => {
    expect(goSources.length).toBeGreaterThan(500)
    const offenders = goSources.flatMap(source => goImportPaths(source.text)
      .filter(isGoTestImport)
      .map(importPath => `${source.path} imports ${importPath}`))
    expect(offenders, 'a Go file that the scan counts as production imports test code: move it to a _test.go file or a test-support package').toEqual([])
  })
})

/**
 * A whole-word search. `\b` treats `_` as a word character, so `ZCODE_STORED_PART`
 * does NOT match inside `ZCODE_STORED_PART_STATUS` -- which two of these tables need,
 * because one table's name is a prefix of another's.
 */
function wordPattern(symbol) {
  return new RegExp(`\\b${RegExp.escape(symbol)}\\b`)
}

describe('wordPattern', () => {
  it('matches literal dots and rejects longer underscored symbols', () => {
    expect(wordPattern('contracts.Probe').test('(contracts.Probe)')).toBe(true)
    expect(wordPattern('contracts.Probe').test('contractsXProbe')).toBe(false)
    expect(wordPattern('ZCODE_STORED_PART').test('ZCODE_STORED_PART_STATUS')).toBe(false)
    expect(wordPattern('ZCODE_STORED_PART').test('(ZCODE_STORED_PART)')).toBe(true)
  })
})

function mentions(sources, symbol) {
  const pattern = wordPattern(symbol)
  return sources.filter(source => pattern.test(source.text)).map(source => source.path)
}

const contracts = new Map(PROVIDER_PROTOCOLS.map(spec =>
  [
    spec.name,
    JSON.parse(readFileSync(join(root, 'contracts', `${spec.name}.json`), 'utf8')),
  ]))

/**
 * The generated structs that draw one KEY of one table, plus every struct that
 * reaches them through a `#Name` field.
 *
 * A nested record is a real reader of its keys, and the Go code mentions the OUTER
 * struct alone -- `providers/zcode/tool_store.go` decodes a `ZCodeStoredPart`, and that is what
 * reads `ZCodePartState`'s fields. Without the transitive step every nested record
 * read as unread.
 */
function structsThatRead(contract, table, key) {
  const structs = contract.structs ?? {}
  const direct = Object.entries(structs)
    .filter(([, definition]) => definition.fields.some(field => field.key === key && (field.table ?? definition.table) === table))
    .map(([name]) => name)
  const reached = new Set(direct)
  let grew = true
  while (grew) {
    grew = false
    for (const [name, definition] of Object.entries(structs)) {
      if (reached.has(name))
        continue
      const refersToReached = definition.fields.some(field => [...reached].some(target => field.type.endsWith(`#${target}`)))
      if (refersToReached) {
        reached.add(name)
        grew = true
      }
    }
  }
  return [...reached]
}

/** Follow each startup descriptor to its exact option-ID and value keys. */
function startupGroupReaders(spec, table, contract, key, side, sources) {
  const groups = Object.entries(contract.startupOptionGroups ?? {}).filter(([, group]) => {
    if (table.key === 'optionIds')
      return key === undefined || group.id === key
    return group.valuesTable === table.key && (key === undefined || group.options.some(option => option.value === key))
  })
  if (groups.length === 0)
    return []
  const symbol = side === 'go'
    ? `contracts.${spec.goPrefix}StartupOptionGroups`
    : `${spec.tsPrefix}_STARTUP_OPTION_GROUPS`
  return sources.filter((source) => {
    if (side === 'go')
      return new RegExp(`range\\s+${symbol.replaceAll('.', '\\.')}\\b`).test(source.text)
    return tsLocalNames(source.text, symbol).some(local =>
      tsIteratesEveryValue(source.text, local)
      || groups.some(([group]) => wordPattern(`${local}.${group}`).test(source.text)))
  }).map(source => source.path)
}

/** A read subset also reads the parent keys that it selects on that language side. */
function subsetReaders(spec, table, contract, key, side, sources, context = { protocols: PROVIDER_PROTOCOLS, contracts }) {
  const protocols = [
    spec,
    ...context.protocols.filter(candidate => candidate !== spec && (spec.name === undefined || candidate.name !== spec.name)),
  ]
  return protocols.flatMap((owner) => {
    const ownerData = owner === spec
      ? contract
      : context.contracts.get(owner.name)
    return owner.tables.filter((subset) => {
      if (subset.subsetOf === undefined || !tableReaders(subset).includes(side))
        return false
      const parent = resolveProviderSubsetParent(owner, subset, protocols)
      const sameOwner = parent.spec === spec || (spec.name !== undefined && parent.spec.name === spec.name)
      const members = ownerData?.[subset.key]
      return sameOwner && parent.table.key === table.key && Array.isArray(members) && members.length > 0
        && (key === undefined || members.includes(key))
    }).flatMap(subset => mentions(sources, side === 'go'
      ? `contracts.${owner.goPrefix}${subset.goTable}Members`
      : `${owner.tsPrefix}_${subset.tsTable}`))
  })
}

/** The Go files that read one key of one table. */
function goKeyReaders(spec, table, contract, key) {
  const direct = mentions(goSources, `contracts.${spec.goPrefix}${table.goTable}${key}`)
  if (direct.length > 0)
    return direct
  // A `goSlice` table also emits its ordered key slice, and a caller that iterates
  // that slice reads EVERY key of the table.
  if (table.goSlice) {
    const iterated = mentions(goSources, `contracts.${spec.goPrefix}${table.goTable}Keys`)
    if (iterated.length > 0)
      return iterated
  }
  // A pin may also check vocabulary that production never reads. An explicit
  // unread reason still applies to that test-only mention, but not a real reader.
  if (table.goTagPin && !unreadKeys(contract).get(`${table.key}.${key}`)?.has('go')
    && tagPinReaders(spec, table, contract).includes(key)) {
    return [table.goTagPin]
  }
  return [
    ...structsThatRead(contract, table.key, key).flatMap(name => mentions(goSources, `contracts.${name}`)),
    ...startupGroupReaders(spec, table, contract, key, 'go', goSources),
    ...subsetReaders(spec, table, contract, key, 'go', goSources),
  ]
}

/**
 * The names one file can spell a generated table under.
 *
 * The browser routinely imports a table under a LOCAL alias and re-exports it with
 * its own key spellings -- `import { ACP_TOOL_KIND as ACP_TOOL_KIND_WORD }`, then
 * `EXECUTE: ACP_TOOL_KIND_WORD.Execute`. Reading the alias out of the import is what
 * keeps that from counting as unread, and it stays EXACT: a plain `.Execute` anywhere
 * in the file would credit this table for a key that a DIFFERENT table's constant
 * spells, which is the very drift these tests exist to find.
 */
function tsLocalNames(text, symbol) {
  const aliases = [
    ...text.matchAll(new RegExp(`\\b${symbol}\\s+as\\s+(\\w+)`, 'g')),
  ].map(match => match[1])
  return [symbol, ...aliases]
}

/**
 * Whether a TS source hands every value of a table to its caller, under the local name
 * `local`: `Object.values(LOCAL)` or `Object.entries(LOCAL)`.
 *
 * Such a file reads EVERY key of the table. This is the browser's form of the ordered
 * key slice that a Go `goSlice` table emits: `new Set(Object.values(...))` accepts
 * each value of the table, so a key that the contract adds joins the set.
 */
function tsIteratesEveryValue(text, local) {
  return new RegExp(`\\bObject\\.(?:values|entries)\\(\\s*${RegExp.escape(local)}\\s*\\)`).test(text)
}

describe('tsIteratesEveryValue', () => {
  it('recognizes each form that hands over every value', () => {
    expect(tsIteratesEveryValue('new Set<string>(Object.values(PROBE_STEP_TYPE))', 'PROBE_STEP_TYPE')).toBe(true)
    expect(tsIteratesEveryValue('for (const [k, v] of Object.entries( PROBE_STEP_TYPE )) {}', 'PROBE_STEP_TYPE')).toBe(true)
  })

  it('rejects a key list, another table and a nested value', () => {
    expect(tsIteratesEveryValue('Object.keys(PROBE_STEP_TYPE)', 'PROBE_STEP_TYPE')).toBe(false)
    expect(tsIteratesEveryValue('Object.values(PROBE_STEP_TYPE_OTHER)', 'PROBE_STEP_TYPE')).toBe(false)
    expect(tsIteratesEveryValue('Object.values(OTHER_PROBE_STEP_TYPE)', 'PROBE_STEP_TYPE')).toBe(false)
    expect(tsIteratesEveryValue('Object.values(PROBE_STEP_TYPE.Nested)', 'PROBE_STEP_TYPE')).toBe(false)
    expect(tsIteratesEveryValue('import { PROBE_STEP_TYPE } from \'generated\'', 'PROBE_STEP_TYPE')).toBe(false)
  })
})

/** The TS files that read one key of one table. */
function tsKeyReaders(spec, table, contract, key) {
  const symbol = `${spec.tsPrefix}_${table.tsTable}`
  const direct = tsSources
    .filter(source => tsLocalNames(source.text, symbol).some(local =>
      wordPattern(`${local}.${key}`).test(source.text) || tsIteratesEveryValue(source.text, local)))
    .map(source => source.path)
  return [
    ...direct,
    ...subsetReaders(spec, table, contract, key, 'ts', tsSources),
  ]
}

function keyReaders(spec, table, contract, key, side) {
  return side === 'go'
    ? goKeyReaders(spec, table, contract, key)
    : [
        ...tsKeyReaders(spec, table, contract, key),
        ...startupGroupReaders(spec, table, contract, key, 'ts', tsSources),
      ]
}

/**
 * The Go readers of one table: any constant it emits, the ordered key slice a
 * `goSlice` table emits, or a generated STRUCT whose tags come from the table. The
 * struct is a real reader -- its `json:"..."` tags ARE the table's values, so a rename
 * moves them and Go keeps matching.
 */
function goReaders(spec, table, contract) {
  if (table.subsetOf !== undefined)
    return mentions(goSources, `contracts.${spec.goPrefix}${table.goTable}Members`)
  const keys = Object.keys(contract[table.key] ?? {})
  const constants = keys.map(key => `contracts.${spec.goPrefix}${table.goTable}${key}`)
  const symbols = [...constants]
  if (table.goSlice)
    symbols.push(`contracts.${spec.goPrefix}${table.goTable}Keys`)
  for (const [name, definition] of Object.entries(contract.structs ?? {})) {
    const backedByTable = definition.table === table.key
      || definition.fields.some(field => field.table === table.key)
    if (backedByTable)
      symbols.push(`contracts.${name}`)
  }
  return [
    ...symbols.flatMap(symbol => mentions(goSources, symbol)),
    ...startupGroupReaders(spec, table, contract, undefined, 'go', goSources),
    ...subsetReaders(spec, table, contract, undefined, 'go', goSources),
  ]
}

function tsReaders(spec, table, contract) {
  return [
    ...mentions(tsSources, `${spec.tsPrefix}_${table.tsTable}`),
    ...startupGroupReaders(spec, table, contract, undefined, 'ts', tsSources),
    ...subsetReaders(spec, table, contract, undefined, 'ts', tsSources),
  ]
}

describe('goKeyReaders', () => {
  const spec = PROVIDER_PROTOCOLS.find(spec => spec.name === 'kimi-protocol')
  const table = spec.tables.find(table => table.key === 'conversationFields')
  const contract = contracts.get(spec.name)

  it('recognizes the declared reflection check for one native decoder field', () => {
    expect(goKeyReaders(spec, table, contract, 'ItemID')).toEqual([table.goTagPin])
  })

  it('keeps a test-only vocabulary check from invalidating an unread reason', () => {
    const unread = {
      ...contract,
      _unread: {
        conversationFields: {
          ItemID: {
            sides: ['go'],
            why: 'The probe has no production reader for this field.',
          },
        },
      },
    }
    expect(goKeyReaders(spec, table, unread, 'ItemID')).toEqual([])
  })

  it('retains a real production reader even when the contract excuses it', () => {
    const unread = {
      ...contract,
      _unread: {
        conversationFields: {
          SessionID: {
            sides: ['go'],
            why: 'The probe incorrectly excuses a field that production reads.',
          },
        },
      },
    }
    expect(goKeyReaders(spec, table, unread, 'SessionID')).not.toContain(table.goTagPin)
    expect(goKeyReaders(spec, table, unread, 'SessionID').length).toBeGreaterThan(0)
  })
})

/**
 * The keys of a `goTagPin` table that its named Go test still pins.
 *
 * The answer is empty when the test moved or stopped citing the table, which is what
 * makes the exemption self-checking: the pin has to keep pointing at a real test.
 */
function tagPinReaders(spec, table, contract) {
  const path = join(root, table.goTagPin)
  expect(existsSync(path), `contracts table ${table.key} states goTagPin ${table.goTagPin}, which does not exist -- point it at the test that pins the tags, or delete the pin and wire a production reader`).toBe(true)
  const text = stripComments(readFileSync(path, 'utf8'))
  return Object.keys(contract[table.key] ?? {})
    .filter(key => wordPattern(`contracts.${spec.goPrefix}${table.goTable}${key}`).test(text))
}

const cases = PROVIDER_PROTOCOLS.flatMap(spec =>
  spec.tables.map(table => ({ spec, table, name: `${spec.name}.${table.key}` })))

describe('every generated contract table has a reader on each side it declares', () => {
  it('covers every declared table', () => {
    expect(cases.length).toBeGreaterThan(50)
  })

  it.each(cases)('$name', ({ spec, table }) => {
    const contract = contracts.get(spec.name)
    const readers = tableReaders(table)
    const found = {
      go: readers.includes('go')
        ? goReaders(spec, table, contract)
        : [],
      ts: readers.includes('ts')
        ? tsReaders(spec, table, contract)
        : [],
    }
    // A tag-pinned table's Go reader is a hand-written struct whose tags a reflection
    // test holds to this table. That test is not production code, so the sweep above
    // cannot see it; the table states the file, and the pin is checked instead.
    if (table.goTagPin != null)
      found.go = tagPinReaders(spec, table, contract)
    for (const side of readers) {
      expect(found[side].length, [
        `contracts/${spec.name}.json table ${JSON.stringify(table.key)} declares a ${side} reader and has none.`,
        `Either a call site spells the literal by hand instead of importing what the table emits`,
        `-- wire that call site -- or the side genuinely does not read it, and the table must say`,
        `readers: ['${readers.filter(other => other !== side).join('\', \'')}'] with a readersWhy in PROVIDER_PROTOCOLS.`,
      ].join(' ')).toBeGreaterThan(0)
    }
  })
})

/**
 * Every KEY must reach a reader, not only the table.
 *
 * A table-level check passes on one used key, so a key that nothing reads rides along
 * with its neighbours -- which is how a generated struct can omit two fields of its
 * own table while both languages keep spelling those two by hand. Two rules, because
 * the tables are two kinds of thing:
 *
 *   - A RECORD (one a generated struct draws its tags from) must reach a reader on
 *     EACH side it declares. The struct states the field list, so a key outside it is
 *     a field nothing deserializes.
 *   - A CATALOG (every other table: event types, tool names, modes) must reach a
 *     reader on at least ONE side. Each side dispatches on the subset it draws a row
 *     for, and holding all ~700 catalog keys to both sides would demand a written
 *     justification for 230 of them -- which moves the burden onto the normal case
 *     and buys no drift that the one-side rule misses.
 *
 * A key that one side genuinely never reads states that in the contract's `_unread`
 * block, with the reason. An exemption the code no longer needs fails here too, so a
 * stale one cannot survive a call site that starts reading the key.
 */
describe('every key of a generated contract table has a reader', () => {
  const keyCases = cases.filter(({ table }) => table.subsetOf === undefined).flatMap(({ spec, table, name }) => {
    const contract = contracts.get(spec.name)
    const structTables = new Set(Object.values(contract.structs ?? {})
      .flatMap(definition => [
        definition.table,
        ...definition.fields.map(field => field.table ?? definition.table),
      ]))
    const excused = unreadKeys(contract)
    return Object.keys(contract[table.key] ?? {}).map(key => ({
      spec,
      table,
      contract,
      key,
      excused: excused.get(`${table.key}.${key}`) ?? new Set(),
      isRecord: structTables.has(table.key),
      name: `${name}.${key}`,
    }))
  })

  it('covers every declared key', () => {
    expect(keyCases.length).toBeGreaterThan(400)
  })

  it.each(keyCases)('$name', ({ spec, table, contract, key, excused, isRecord }) => {
    const readers = tableReaders(table)
    const found = new Map(readers.map(side => [
      side,
      keyReaders(spec, table, contract, key, side),
    ]))
    for (const side of excused) {
      expect(found.get(side)?.length ?? 0, [
        `contracts/${spec.name}.json _unread.${table.key}.${key} excuses the ${side} side, which now reads the key`,
        `at ${(found.get(side) ?? []).join(', ')}. Delete the exemption: an exemption nothing needs is a claim that stopped being true.`,
      ].join(' ')).toBe(0)
    }
    const required = readers.filter(side => !excused.has(side))
    // Every declared side excused: the key is a member of a closed set that the
    // contract records and neither language spells. `_unread` already carries the
    // reason, and the generator holds that reason to a real sentence.
    if (required.length === 0)
      return
    const read = required.filter(side => found.get(side).length > 0)
    const explain = [
      `contracts/${spec.name}.json ${table.key}.${key} reaches no reader.`,
      `Either a call site spells ${JSON.stringify(contract[table.key][key])} by hand instead of importing`,
      `contracts.${spec.goPrefix}${table.goTable}${key} / ${spec.tsPrefix}_${table.tsTable}.${key} -- wire that call site --`,
      `or one side genuinely does not read it, and contracts/${spec.name}.json must say so in`,
      `_unread.${table.key}.${key} with the reason.`,
    ].join(' ')
    if (isRecord) {
      // A struct-backed table is a record: each side decodes the whole of it.
      for (const side of required)
        expect(found.get(side).length, `${explain} The ${side} side reads none of it, and a generated struct draws this table.`).toBeGreaterThan(0)
      return
    }
    expect(read.length, explain).toBeGreaterThan(0)
  })
})

describe('a one-sided table states why', () => {
  it.each(PROVIDER_PROTOCOLS.flatMap(spec =>
    spec.tables.filter(table => new Set(tableReaders(table)).size === 1)
      .map(table => ({ name: `${spec.name}.${table.key}`, table }))))('$name', ({ table }) => {
    // The generator enforces this too. Repeated here so the failure identifies the table
    // rather than the whole domain, and so the reason survives a generator refactor.
    expect(typeof table.readersWhy).toBe('string')
    expect(table.readersWhy.length).toBeGreaterThan(20)
  })
})

describe('startupGroupReaders', () => {
  const spec = { goPrefix: 'Probe', tsPrefix: 'PROBE' }
  const contract = {
    startupOptionGroups: {
      Mode: {
        id: 'Mode',
        valuesTable: 'modes',
        options: [
          { value: 'Native' },
        ],
      },
    },
  }
  const source = text => [
    { path: 'consumer', text },
  ]

  it('follows real consumers in both languages', () => {
    expect(startupGroupReaders(spec, { key: 'optionIds' }, contract, 'Mode', 'go', source('for _, group := range contracts.ProbeStartupOptionGroups {}'))).toEqual(['consumer'])
    expect(startupGroupReaders(spec, { key: 'modes' }, contract, 'Native', 'ts', source('Object.values(PROBE_STARTUP_OPTION_GROUPS)'))).toEqual(['consumer'])
    expect(startupGroupReaders(spec, { key: 'modes' }, contract, 'Native', 'ts', source('PROBE_STARTUP_OPTION_GROUPS.Mode'))).toEqual(['consumer'])
  })

  it('rejects an import without a consumer', () => {
    expect(startupGroupReaders(spec, { key: 'modes' }, contract, 'Native', 'ts', source('import { PROBE_STARTUP_OPTION_GROUPS } from \'generated\''))).toEqual([])
    expect(startupGroupReaders(spec, { key: 'modes' }, contract, 'Native', 'go', [])).toEqual([])
  })

  it('keeps unrelated axes and values separate', () => {
    const sources = source('PROBE_STARTUP_OPTION_GROUPS.Other')
    expect(startupGroupReaders(spec, { key: 'modes' }, contract, 'Native', 'ts', sources)).toEqual([])
    expect(startupGroupReaders(spec, { key: 'modes' }, contract, 'Other', 'ts', source('Object.values(PROBE_STARTUP_OPTION_GROUPS)'))).toEqual([])
    expect(startupGroupReaders(spec, { key: 'other' }, contract, 'Native', 'ts', source('Object.values(PROBE_STARTUP_OPTION_GROUPS)'))).toEqual([])
  })
})

describe('subsetOf readers', () => {
  const parent = { key: 'events', goTable: 'Event', tsTable: 'EVENT' }
  const subset = {
    key: 'notices',
    subsetOf: 'events',
    goTable: 'ProbeNotice',
    tsTable: 'PROBE_NOTICE',
  }
  const spec = {
    goPrefix: 'SubsetProbe',
    tsPrefix: 'SUBSET_PROBE',
    tables: [parent, subset],
  }
  const contract = {
    events: { A: 'a', B: 'b' },
    notices: ['B'],
  }

  it('credits the parent keys that a read subset lists', () => {
    const go = [
      { path: 'go-reader', text: 'contracts.SubsetProbeProbeNoticeMembers' },
    ]
    const ts = [
      { path: 'ts-reader', text: 'SUBSET_PROBE_PROBE_NOTICE.has(value)' },
    ]
    expect(subsetReaders(spec, parent, contract, 'B', 'go', go)).toEqual(['go-reader'])
    expect(subsetReaders(spec, parent, contract, 'B', 'ts', ts)).toEqual(['ts-reader'])
    expect(subsetReaders(spec, parent, contract, 'A', 'go', go)).toEqual([])
    expect(subsetReaders(spec, parent, contract, 'A', 'ts', ts)).toEqual([])
  })

  it('reports an unread subset', () => {
    expect(goReaders(spec, subset, contract)).toEqual([])
    expect(tsReaders(spec, subset, contract)).toEqual([])
  })

  it('credits only the language sides that the subset declares', () => {
    const goOnly = {
      ...spec,
      tables: [
        parent,
        {
          ...subset,
          readers: ['go'],
        },
      ],
    }
    const sources = [
      { path: 'reader', text: 'SUBSET_PROBE_PROBE_NOTICE.has(value)' },
    ]
    expect(subsetReaders(goOnly, parent, contract, 'B', 'ts', sources)).toEqual([])
  })

  it('credits only selected parent keys through a foreign subset', () => {
    const subject = {
      ...spec,
      name: 'parent-protocol',
      tables: [parent],
    }
    const consumer = {
      name: 'consumer-protocol',
      goPrefix: 'Consumer',
      tsPrefix: 'CONSUMER',
      tables: [
        { ...subset, subsetOf: 'parent-protocol.events' },
      ],
    }
    const context = {
      protocols: [subject, consumer],
      contracts: new Map([
        [
          consumer.name,
          {
            notices: ['B'],
          },
        ],
      ]),
    }
    const go = [
      { path: 'foreign-go', text: 'contracts.ConsumerProbeNoticeMembers' },
    ]
    const ts = [
      { path: 'foreign-ts', text: 'CONSUMER_PROBE_NOTICE.has(value)' },
    ]
    expect(subsetReaders(subject, parent, contract, 'B', 'go', go, context)).toEqual(['foreign-go'])
    expect(subsetReaders(subject, parent, contract, 'B', 'ts', ts, context)).toEqual(['foreign-ts'])
    expect(subsetReaders(subject, parent, contract, 'A', 'go', go, context)).toEqual([])
    expect(subsetReaders(subject, parent, contract, 'A', 'ts', ts, context)).toEqual([])
  })

  it('does not credit a foreign subset on an undeclared language side', () => {
    const subject = {
      ...spec,
      name: 'parent-protocol',
      tables: [parent],
    }
    const consumer = {
      name: 'consumer-protocol',
      goPrefix: 'Consumer',
      tsPrefix: 'CONSUMER',
      tables: [
        {
          ...subset,
          subsetOf: 'parent-protocol.events',
          readers: ['go'],
        },
      ],
    }
    const context = {
      protocols: [subject, consumer],
      contracts: new Map([
        [
          consumer.name,
          {
            notices: ['B'],
          },
        ],
      ]),
    }
    const sources = [
      { path: 'foreign-reader', text: 'CONSUMER_PROBE_NOTICE.has(value)' },
    ]
    expect(subsetReaders(subject, parent, contract, 'B', 'ts', sources, context)).toEqual([])
  })

  it('does not credit matching keys from another parent table', () => {
    const other = {
      ...parent,
      key: 'other',
      goTable: 'Other',
      tsTable: 'OTHER',
    }
    const subject = {
      ...spec,
      name: 'parent-protocol',
      tables: [parent, other],
    }
    const consumer = {
      name: 'consumer-protocol',
      goPrefix: 'Consumer',
      tsPrefix: 'CONSUMER',
      tables: [
        { ...subset, subsetOf: 'parent-protocol.other' },
      ],
    }
    const context = {
      protocols: [subject, consumer],
      contracts: new Map([
        [
          consumer.name,
          {
            notices: ['B'],
          },
        ],
      ]),
    }
    const sources = [
      { path: 'foreign-reader', text: 'contracts.ConsumerProbeNoticeMembers' },
    ]
    expect(subsetReaders(subject, parent, contract, 'B', 'go', sources, context)).toEqual([])
    expect(subsetReaders(subject, other, contract, 'B', 'go', sources, context)).toEqual(['foreign-reader'])
  })
})
