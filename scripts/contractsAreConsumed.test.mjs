import { existsSync, readdirSync, readFileSync, statSync } from 'node:fs'
import { dirname, join, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'bun:test'
import { PROVIDER_PROTOCOLS, tableReaders, unreadKeys } from './generate-contracts.mjs'

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

/** Whether a path is a test file of either language. */
function isTestFile(path) {
  return /(?:_test\.go|\.test\.tsx?)$/.test(path)
}

/**
 * Every source file of one tree, minus the generated output and minus the tests.
 *
 * A test is not a reader. A reflection test that pins a hand-written struct tag to a
 * generated constant mentions the constant, and counting that mention let two tables
 * pass with no production reader at all. A table whose Go reader IS such a test says
 * so with `goTagPin`, which this file then holds to the named file.
 */
function sourceFiles(dir, extensions, found = []) {
  for (const entry of readdirSync(dir)) {
    const path = join(dir, entry)
    // `generated` holds the emitted Go and TS. A table citing itself proves nothing.
    if (entry === 'generated' || entry === 'node_modules')
      continue
    if (statSync(path).isDirectory())
      sourceFiles(path, extensions, found)
    else if (extensions.some(extension => entry.endsWith(extension)) && !isTestFile(entry))
      found.push(path)
  }
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
  return files.map(path => ({ path: relative(root, path), text: stripComments(readFileSync(path, 'utf8')) }))
}

const goSources = readAll(sourceFiles(join(root, 'backend/internal'), ['.go']))
const tsSources = readAll(sourceFiles(join(root, 'frontend/src'), ['.ts', '.tsx']))

/**
 * A whole-word search. `\b` treats `_` as a word character, so `ZCODE_STORED_PART`
 * does NOT match inside `ZCODE_STORED_PART_STATUS` -- which two of these tables need,
 * because one table's name is a prefix of another's.
 */
function wordPattern(symbol) {
  return new RegExp(`\\b${symbol.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}\\b`)
}

function mentions(sources, symbol) {
  const pattern = wordPattern(symbol)
  return sources.filter(source => pattern.test(source.text)).map(source => source.path)
}

const contracts = new Map(PROVIDER_PROTOCOLS.map(spec =>
  [spec.name, JSON.parse(readFileSync(join(root, 'contracts', `${spec.name}.json`), 'utf8'))]))

/**
 * The generated structs that draw one KEY of one table, plus every struct that
 * reaches them through a `#Name` field.
 *
 * A nested record is a real reader of its keys, and the Go code mentions the OUTER
 * struct alone -- `zcode_tool_store.go` decodes a `ZCodeStoredPart`, and that is what
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
  return structsThatRead(contract, table.key, key).flatMap(name => mentions(goSources, `contracts.${name}`))
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
  const aliases = [...text.matchAll(new RegExp(`\\b${symbol}\\s+as\\s+(\\w+)`, 'g'))].map(match => match[1])
  return [symbol, ...aliases]
}

/** The TS files that read one key of one table. */
function tsKeyReaders(spec, table, key) {
  const symbol = `${spec.tsPrefix}_${table.tsTable}`
  return tsSources
    .filter(source => tsLocalNames(source.text, symbol).some(local => wordPattern(`${local}.${key}`).test(source.text)))
    .map(source => source.path)
}

function keyReaders(spec, table, contract, key, side) {
  return side === 'go' ? goKeyReaders(spec, table, contract, key) : tsKeyReaders(spec, table, key)
}

/**
 * The Go readers of one table: any constant it emits, the ordered key slice a
 * `goSlice` table emits, or a generated STRUCT whose tags come from the table. The
 * struct is a real reader -- its `json:"..."` tags ARE the table's values, so a rename
 * moves them and Go keeps matching.
 */
function goReaders(spec, table, contract) {
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
  return symbols.flatMap(symbol => mentions(goSources, symbol))
}

function tsReaders(spec, table) {
  return mentions(tsSources, `${spec.tsPrefix}_${table.tsTable}`)
}

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
      go: readers.includes('go') ? goReaders(spec, table, contract) : [],
      ts: readers.includes('ts') ? tsReaders(spec, table) : [],
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
  const keyCases = cases.flatMap(({ spec, table, name }) => {
    const contract = contracts.get(spec.name)
    const structTables = new Set(Object.values(contract.structs ?? {})
      .flatMap(definition => [definition.table, ...definition.fields.map(field => field.table ?? definition.table)]))
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
    const found = new Map(readers.map(side => [side, keyReaders(spec, table, contract, key, side)]))
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
