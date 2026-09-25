import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import ts from 'typescript'
import { describe, expect, it } from 'vitest'
import { collectFiles, frontendRoot, posixRelative } from '~/test-support/sourceTree'

// Every provider fills the tool-call model from a TABLE keyed by `ToolKind`: one entry for
// each kind, holding that kind's request or that kind's whole payload. The table's own
// type states which kind each entry answers for, so an entry cannot fill another kind's
// shape and cannot leave a declared field out.
//
// It does NOT state that the entry fills no field the kind never declared, and that is a
// surprise worth writing down. TypeScript runs its excess-property check on a FRESH
// object literal in an ANNOTATED position. The contextual signature a mapped type
// supplies is not one: the compiler infers the arrow's return type FROM the literal it
// returns, so the object loses its freshness before any property is checked. Both of
// these compile, and they are the two forms this guard refuses:
//
//   'fetch': args => ({ url: read(args), patchText: '...' })          // no annotation
//   'fetch': (args): ToolRequestByKind['fetch'] => {
//     const request = { url: read(args), patchText: '...' }           // not a literal
//     return request
//   }
//
// An explicit return type, or a declared type on the intermediate `const`, puts the
// literal back under the check. A `patchText` key once rode `FileChangeRequest` into the
// model through exactly this hole: no renderer could read it, and nothing could report it.
//
// A PARSE rather than a text scan. The tables hold arrow entries, named-function
// entries, factory calls and entry objects with a `build` member, and a regular
// expression that reads all four either misses a form or reports a comment that quotes
// one. The parser also removes the `stripCommentLines` step the text guards beside this
// one need, because a comment is not a node.
//
// WHAT IT DOES NOT CATCH. The rule reaches the entry, an entry object's members, and one
// level of factory. Past that it stops, and three things sit outside it:
//
//   - A HELPER an entry's body calls. `acpGenericCard` and `acpFileChangeParts` each
//     build a request half and hand it back, two levels down. Each declares its own
//     return type, and only a reader keeps them that way.
//   - A `const` that reaches the answer through a call rather than by reference. The
//     rule reads the identifier at the `return`, so `wrap(request)` hides it.
//   - A table whose declaration hides the mapped type behind a type ALIAS. Both forms
//     this guard reads state the key set at the declaration, which is what makes
//     discovery a pattern rather than a name list -- so a table a new provider adds is
//     covered with no edit here, and `KNOWN_TABLES` catches a form that stops matching.

const PROVIDERS_DIR = join(frontendRoot, 'src/components/chat/providers')

/**
 * The tables that exist today, pinned so a rename cannot make the guard vacuous.
 *
 * Discovery is a PATTERN and not this list, so a table a future provider adds is
 * covered with no edit here (see {@link guardedTables}). The list exists for the
 * opposite failure: a declaration form that stops matching reports nothing, which reads
 * exactly like a clean tree. A name here that the walk no longer finds fails the case
 * below and states which table went unread.
 */
const KNOWN_TABLES = [
  'ACP_SPEC_READERS',
  'ACP_TOOL_REQUEST_OVERRIDES',
  'AMP_TOOL_READERS',
  'AMP_TOOL_REQUEST_OVERRIDES',
  'CLAUDE_TOOL_READERS',
  'CLAUDE_TOOL_REQUEST_OVERRIDES',
  'CLINE_TOOL_READERS',
  'CLINE_TOOL_REQUEST_OVERRIDES',
  'CODEWHALE_TOOL_READERS',
  'CODEWHALE_TOOL_REQUEST_OVERRIDES',
  'CODEX_TOOL_READERS',
  'CODEX_TOOL_REQUEST_OVERRIDES',
  'COPILOT_TOOL_READERS',
  'COPILOT_TOOL_REQUEST_OVERRIDES',
  'DEFAULT_TOOL_REQUESTS',
  'KIMI_TOOL_READERS',
  'KIMI_TOOL_REQUEST_OVERRIDES',
  'MIMO_TOOL_READERS',
  'MIMO_TOOL_REQUEST_OVERRIDES',
  'OH_MY_PI_TOOL_READERS',
  'OH_MY_PI_TOOL_REQUEST_OVERRIDES',
  'PI_TOOL_READERS',
  'PI_TOOL_REQUEST_OVERRIDES',
  'ZCODE_TOOL_READERS',
  'ZCODE_TOOL_REQUEST_OVERRIDES',
]

type FunctionNode = ts.ArrowFunction | ts.FunctionExpression | ts.FunctionDeclaration

/** One table the walk found, and the offences its entries carry. */
interface TableScan {
  name: string
  entries: number
  offences: string[]
}

/** The plugin modules a guard reads: the implementation, never its tests or fixtures. */
function providerModules(): string[] {
  return collectFiles(PROVIDERS_DIR, {
    matches: name => (name.endsWith('.ts') || name.endsWith('.tsx'))
      && !name.endsWith('.test.ts')
      && !name.endsWith('.test.tsx')
      // Test data stays with the provider that it describes; see `providers/README.md`.
      && !name.endsWith('.fixtures.ts')
      && name !== 'testUtils.ts'
      && name !== 'testUtils.tsx'
      && name !== 'testMocks.ts',
  })
}

function parse(path: string, text: string): ts.SourceFile {
  return ts.createSourceFile(path, text, ts.ScriptTarget.Latest, true)
}

function asFunction(node: ts.Node): FunctionNode | null {
  return ts.isArrowFunction(node) || ts.isFunctionExpression(node) || ts.isFunctionDeclaration(node) ? node : null
}

/** `(expr)` down to `expr`, so a parenthesized object body reads as the literal it is. */
function unwrapParens(node: ts.Expression): ts.Expression {
  return ts.isParenthesizedExpression(node) ? unwrapParens(node.expression) : node
}

/**
 * Every expression one function hands back, from its concise body or its `return`
 * statements. A nested function's own returns belong to that function, so the walk
 * stops at one.
 */
function returnedExpressions(fn: FunctionNode): ts.Expression[] {
  const body = fn.body
  if (!body)
    return []
  if (!ts.isBlock(body))
    return [body]
  const found: ts.Expression[] = []
  const visit = (node: ts.Node): void => {
    if (asFunction(node))
      return
    if (ts.isReturnStatement(node) && node.expression)
      found.push(node.expression)
    ts.forEachChild(node, visit)
  }
  ts.forEachChild(body, visit)
  return found
}

/** A `cond ? a : b` down to its branches, so both sides of a ladder are read. */
function resultValues(expr: ts.Expression): ts.Expression[] {
  const value = unwrapParens(expr)
  return ts.isConditionalExpression(value)
    ? [...resultValues(value.whenTrue), ...resultValues(value.whenFalse)]
    : [value]
}

/**
 * Whether a function hands back an object LITERAL on any path.
 *
 * This is what separates a builder from a predicate beside it. `needsResult` answers a
 * boolean, which holds no property that could be excess, so it needs no annotation; a
 * `build` that returns `{ kind, request }` does.
 */
function returnsObjectLiteral(fn: FunctionNode): boolean {
  return returnedExpressions(fn).flatMap(resultValues).some(ts.isObjectLiteralExpression)
}

/**
 * The names one returned expression hands OUT by reference, into `into`.
 *
 * A name that merely appears in the return is not one of these. `String(seen)` reads the
 * variable and hands back a string, so nothing the literal carries escapes. A name that
 * IS the answer, or that lands on the answer as a property value, a shorthand property,
 * a spread or an array element, does escape -- and it escapes with every key it holds.
 */
function escapingNames(expr: ts.Expression, into: Set<string>): void {
  const value = unwrapParens(expr)
  if (ts.isConditionalExpression(value)) {
    escapingNames(value.whenTrue, into)
    escapingNames(value.whenFalse, into)
    return
  }
  // `a ?? b` and `a || b` each hand back one side whole, as a ternary does.
  if (ts.isBinaryExpression(value)
    && (value.operatorToken.kind === ts.SyntaxKind.QuestionQuestionToken || value.operatorToken.kind === ts.SyntaxKind.BarBarToken)) {
    escapingNames(value.left, into)
    escapingNames(value.right, into)
    return
  }
  if (ts.isSpreadElement(value)) {
    escapingNames(value.expression, into)
    return
  }
  if (ts.isIdentifier(value)) {
    into.add(value.text)
    return
  }
  if (ts.isArrayLiteralExpression(value)) {
    for (const element of value.elements)
      escapingNames(element, into)
    return
  }
  if (!ts.isObjectLiteralExpression(value))
    return
  for (const member of value.properties) {
    if (ts.isShorthandPropertyAssignment(member))
      into.add(member.name.text)
    else if (ts.isPropertyAssignment(member))
      escapingNames(member.initializer, into)
    else if (ts.isSpreadAssignment(member))
      escapingNames(member.expression, into)
  }
}

/**
 * The names of the un-annotated object-literal `const`s that this function hands back.
 *
 * A variable is not a fresh literal, so lifting a request out of the `return` switches
 * the excess-property check off exactly as a missing return type does. Only a `const`
 * that ESCAPES counts, in the sense {@link escapingNames} gives: a local the function
 * merely reads carries nothing into the model.
 */
function unannotatedReturnedConsts(fn: FunctionNode): string[] {
  const body = fn.body
  if (!body || !ts.isBlock(body))
    return []
  const mentioned = new Set<string>()
  for (const expr of returnedExpressions(fn))
    escapingNames(expr, mentioned)
  const found: string[] = []
  const visit = (node: ts.Node): void => {
    if (asFunction(node))
      return
    if (ts.isVariableDeclaration(node) && !node.type && node.initializer && ts.isIdentifier(node.name)
      && ts.isObjectLiteralExpression(unwrapParens(node.initializer)) && mentioned.has(node.name.text)) {
      found.push(node.name.text)
    }
    ts.forEachChild(node, visit)
  }
  ts.forEachChild(body, visit)
  return found
}

/**
 * Whether a declaration's type is a table over `ToolKind`.
 *
 * Three forms state the key set: an inline mapped type, the shared reader-table alias,
 * and the request-override alias. A provider that adds a table takes one of these, because they
 * are what `toolRequestFor` and the payload lookups accept.
 */
function isToolKindTable(node: ts.TypeNode | undefined): boolean {
  if (!node)
    return false
  if (ts.isTypeReferenceNode(node))
    return ts.isIdentifier(node.typeName) && (node.typeName.text === 'ToolRequestOverrides' || node.typeName.text === 'ToolCallSpecReaderTable')
  if (!ts.isMappedTypeNode(node))
    return false
  const constraint = node.typeParameter.constraint
  return constraint !== undefined && ts.isTypeReferenceNode(constraint)
    && ts.isIdentifier(constraint.typeName) && constraint.typeName.text === 'ToolKind'
}

/** The top-level function one name states in this file, or null when it states none. */
function localFunction(source: ts.SourceFile, name: string): FunctionNode | null {
  for (const statement of source.statements) {
    if (ts.isFunctionDeclaration(statement) && statement.name?.text === name)
      return statement
    if (!ts.isVariableStatement(statement))
      continue
    for (const declaration of statement.declarationList.declarations) {
      if (ts.isIdentifier(declaration.name) && declaration.name.text === name && declaration.initializer) {
        const fn = asFunction(unwrapParens(declaration.initializer))
        if (fn)
          return fn
      }
    }
  }
  return null
}

/** The work one file's scan carries around: where to report, and what to report into. */
interface Scan {
  source: ts.SourceFile
  relative: string
  offences: string[]
}

function at(scan: Scan, node: ts.Node): string {
  return `${scan.relative}:${scan.source.getLineAndCharacterOfPosition(node.getStart(scan.source)).line + 1}`
}

/** Require the return type, and refuse an un-annotated `const` the body hands back. */
function checkBuilder(scan: Scan, fn: FunctionNode, label: string): void {
  if (!fn.type)
    scan.offences.push(`${at(scan, fn)} ${label} declares no return type`)
  for (const name of unannotatedReturnedConsts(fn))
    scan.offences.push(`${at(scan, fn)} ${label} returns \`${name}\`, an object literal that declares no type`)
}

/**
 * The factory an entry delegates to, one level deep.
 *
 * `zcodeArgumentsOnly('wait')` and `acpArgumentsOnly('chart')` build the entry rather
 * than being one, so the annotation the entry needs sits on what the factory RETURNS --
 * an arrow, or an entry object with a `build` member.
 */
function checkFactory(scan: Scan, name: string, site: ts.Node, label: string): void {
  const factory = localFunction(scan.source, name)
  if (!factory) {
    scan.offences.push(`${at(scan, site)} ${label} calls \`${name}\`, which this file does not declare`)
    return
  }
  for (const returned of returnedExpressions(factory).flatMap(resultValues)) {
    const fn = asFunction(returned)
    if (fn)
      checkBuilder(scan, fn, `${label} via \`${name}\``)
    else if (ts.isObjectLiteralExpression(returned))
      checkEntryObject(scan, returned, `${label} via \`${name}\``)
    else
      scan.offences.push(`${at(scan, returned)} ${label} via \`${name}\` hands back a form this guard does not read`)
  }
}

/**
 * An entry that is an OBJECT rather than a function: the Agent Client Protocol's
 * `{ build, needsResult, ownsFailure }`.
 *
 * Every member that builds a literal is checked; a predicate member is not, for the
 * reason {@link returnsObjectLiteral} gives.
 */
function checkEntryObject(scan: Scan, entry: ts.ObjectLiteralExpression, label: string): void {
  for (const member of entry.properties) {
    if (ts.isSpreadAssignment(member)) {
      const call = unwrapParens(member.expression)
      if (ts.isCallExpression(call) && ts.isIdentifier(call.expression))
        checkFactory(scan, call.expression.text, member, label)
      continue
    }
    if (!ts.isPropertyAssignment(member) || !ts.isIdentifier(member.name))
      continue
    const value = unwrapParens(member.initializer)
    const fn = asFunction(value)
    if (fn && returnsObjectLiteral(fn))
      checkBuilder(scan, fn, `${label}.${member.name.text}`)
    else if (ts.isIdentifier(value))
      checkNamedEntry(scan, value, `${label}.${member.name.text}`, true)
  }
}

/** An entry that states a named function. The annotation sits on that declaration. */
function checkNamedEntry(scan: Scan, name: ts.Identifier, label: string, literalOnly: boolean): void {
  const fn = localFunction(scan.source, name.text)
  if (!fn) {
    scan.offences.push(`${at(scan, name)} ${label} states \`${name.text}\`, which this file does not declare`)
    return
  }
  if (literalOnly && !returnsObjectLiteral(fn))
    return
  checkBuilder(scan, fn, `${label} (\`${name.text}\`)`)
}

/** One table entry, in each of the four forms the tables use. */
function checkEntry(scan: Scan, key: string, value: ts.Expression, table: string): void {
  const label = `${table}['${key}']`
  const entry = unwrapParens(value)
  const fn = asFunction(entry)
  if (fn) {
    checkBuilder(scan, fn, label)
    return
  }
  if (ts.isObjectLiteralExpression(entry)) {
    checkEntryObject(scan, entry, label)
    return
  }
  if (ts.isIdentifier(entry)) {
    checkNamedEntry(scan, entry, label, false)
    return
  }
  if (ts.isCallExpression(entry) && ts.isIdentifier(entry.expression)) {
    checkFactory(scan, entry.expression.text, entry, label)
    return
  }
  // A form nobody has written yet. Reporting it is the point: an entry shape this guard
  // cannot read must not pass as one it read and approved.
  scan.offences.push(`${at(scan, entry)} ${label} takes a form this guard does not read`)
}

/** Every `ToolKind` table one source declares, with the offences of its entries. */
function scanTables(relative: string, text: string): TableScan[] {
  const source = parse(relative, text)
  const tables: TableScan[] = []
  for (const statement of source.statements) {
    if (!ts.isVariableStatement(statement))
      continue
    for (const declaration of statement.declarationList.declarations) {
      if (!isToolKindTable(declaration.type) || !declaration.initializer || !ts.isIdentifier(declaration.name))
        continue
      const table = unwrapParens(declaration.initializer)
      if (!ts.isObjectLiteralExpression(table))
        continue
      const scan: Scan = { source, relative, offences: [] }
      const name = declaration.name.text
      let entries = 0
      for (const member of table.properties) {
        if (ts.isPropertyAssignment(member)) {
          entries++
          const key = ts.isStringLiteral(member.name) || ts.isIdentifier(member.name) ? member.name.text : '?'
          checkEntry(scan, key, member.initializer, name)
          continue
        }
        // `{ read }` states a local function under its own name.
        if (ts.isShorthandPropertyAssignment(member)) {
          entries++
          checkNamedEntry(scan, member.name, `${name}['${member.name.text}']`, false)
          continue
        }
        // A SPREAD carries entries this guard cannot see one by one, and no type refuses
        // it here -- `ToolRequestOverrides` says so at its own definition. Report it, so
        // a table that grew a spread is a decision somebody makes rather than a silence.
        if (ts.isSpreadAssignment(member))
          scan.offences.push(`${at(scan, member)} ${name} spreads a value, so this guard cannot read the entries it carries`)
      }
      tables.push({ name: declaration.name.text, entries, offences: scan.offences })
    }
  }
  return tables
}

/** Every table across the plugin layer, in one walk. */
function guardedTables(): TableScan[] {
  return providerModules().flatMap((file) => {
    const relative = posixRelative(frontendRoot, file)
    return scanTables(relative, readFileSync(file, 'utf8'))
  })
}

// The samples below pin the matcher itself. A guard whose matcher stops matching reads
// exactly like a clean tree -- green either way -- so each form it must report and each
// form it must leave alone is stated here as source it parses.

const SAMPLE_TABLE_HEADER = 'const T: { [P in ToolKind]: Reader<P> } = {\n'

function offencesOf(body: string, extra = ''): string[] {
  const tables = scanTables('sample.ts', `${SAMPLE_TABLE_HEADER}${body}}\n${extra}`)
  return tables.flatMap(table => table.offences)
}

describe('a tool-call table entry declares its return type', () => {
  it('finds every table it guards', () => {
    const found = guardedTables()
    const names = found.map(table => table.name).sort()
    expect(
      KNOWN_TABLES.filter(name => !names.includes(name)),
      'The walk stopped finding these tables, so nothing checked their entries. Repoint the '
      + 'declaration form in `isToolKindTable`, or drop the name if the table is gone.',
    ).toEqual([])
    // A table found with no entry read is the same vacuum one level down.
    // `CODEX_TOOL_REQUEST_OVERRIDES` is the one real empty: Codex deviates on no kind, so
    // its map is `{}` and every kind takes the shared table.
    expect(
      found.filter(table => table.name !== 'CODEX_TOOL_REQUEST_OVERRIDES' && table.entries === 0).map(table => table.name),
      'This table was found and no entry was read out of it, so nothing checked it.',
    ).toEqual([])
  })

  it('declares a return type at every entry of every table', () => {
    const offences = guardedTables().flatMap(table => table.offences)
    expect(
      offences,
      'A mapped table states which kind an entry answers for. It does NOT put the entry\'s '
      + 'literal under the excess-property check, because a contextual signature is not an '
      + 'annotated position -- so an entry with no return type accepts a key the kind never '
      + 'declared, and no renderer can read it. Write `(args): ToolRequestByKind[\'fetch\'] => ...` '
      + 'at the entry, and declare the type of any `const` the body hands back.',
    ).toEqual([])
  })

  it('reads an un-annotated entry, in each form a table uses', () => {
    // A bare arrow, a parenthesized arrow, and a named function the entry states.
    expect(offencesOf('  \'fetch\': args => ({ url: args.url }),\n')).toHaveLength(1)
    expect(offencesOf('  \'fetch\': (args) => ({ url: args.url }),\n')).toHaveLength(1)
    expect(offencesOf(
      '  \'mcp\': buildMcp,\n',
      'function buildMcp(facts) {\n  return { kind: \'mcp\' }\n}\n',
    )).toHaveLength(1)
    // A factory, whose annotation belongs on what it hands back.
    expect(offencesOf(
      '  \'wait\': declaredOnly(\'wait\'),\n',
      'function declaredOnly(kind) {\n  return facts => ({ kind })\n}\n',
    )).toHaveLength(1)
    // An entry OBJECT, whose annotation belongs on the member that builds.
    expect(offencesOf('  \'edit\': { needsResult: r => !r.path, build: facts => ({ kind: \'edit\' }) },\n')).toHaveLength(1)
    // The same entry object assembled by a factory and then extended.
    expect(offencesOf(
      '  \'todo\': { ...argumentsOnly(\'todo\'), needsResult: () => true },\n',
      'function argumentsOnly(kind) {\n  return { build: facts => ({ kind }) }\n}\n',
    )).toHaveLength(1)
    // A shorthand entry, which states a local function under its own name.
    expect(offencesOf('  fetch,\n', 'function fetch(args) {\n  return { url: args.url }\n}\n')).toHaveLength(1)
  })

  // Two forms the guard reads no entry out of at all. Both must REPORT rather than pass,
  // because a form it cannot read is not a form it checked.
  it('reads a spread and an unknown entry form as offences of their own', () => {
    expect(offencesOf('  ...SHARED,\n  \'fetch\': (args): Req => ({ url: args.url }),\n')).toHaveLength(1)
    expect(offencesOf('  \'fetch\': helpers.fetch,\n')).toHaveLength(1)
  })

  // Four ways a lifted literal reaches the answer, and the annotation is missing in each.
  it('reads an un-annotated const that an annotated entry hands back', () => {
    const lifted = '    const request = { url: args.url }\n'
    expect(offencesOf(`  'fetch': (args): Req => {\n${lifted}    return { kind: 'fetch', request }\n  },\n`)).toHaveLength(1)
    expect(offencesOf(`  'fetch': (args): Req => {\n${lifted}    return { kind: 'fetch', request: request }\n  },\n`)).toHaveLength(1)
    expect(offencesOf(`  'fetch': (args): Req => {\n${lifted}    return { kind: 'fetch', ...request }\n  },\n`)).toHaveLength(1)
    expect(offencesOf(`  'fetch': (args): Req => {\n${lifted}    return request\n  },\n`)).toHaveLength(1)
    // Through a ladder, which is the shape every lifecycle branch in these tables takes.
    expect(offencesOf(`  'fetch': (args): Req => {\n${lifted}    return args.done ? { kind: 'fetch', request } : { kind: 'fetch', request: {} }\n  },\n`)).toHaveLength(1)
  })

  it('reads no entry that already declares what it hands back', () => {
    expect(offencesOf('  \'fetch\': (args): Req => ({ url: args.url }),\n')).toEqual([])
    expect(offencesOf(
      '  \'fetch\': (args): Req => {\n'
      + '    const request: Req = { url: args.url }\n'
      + '    return { kind: \'fetch\', request }\n'
      + '  },\n',
    )).toEqual([])
    expect(offencesOf(
      '  \'mcp\': buildMcp,\n',
      'function buildMcp(facts): Payload<\'mcp\'> {\n  return { kind: \'mcp\' }\n}\n',
    )).toEqual([])
    expect(offencesOf(
      '  \'wait\': declaredOnly(\'wait\'),\n',
      'function declaredOnly(kind) {\n  return (facts): Payload<P> => ({ kind })\n}\n',
    )).toEqual([])
    expect(offencesOf('  \'edit\': { needsResult: r => !r.path, build: (facts): Payload<\'edit\'> => ({ kind: \'edit\' }) },\n')).toEqual([])
  })

  // Three neutral forms. A rule that reported one of them would teach the next reader to
  // widen an allow-list rather than to annotate a builder.
  it('reads nothing into a predicate, a local, or a table over another union', () => {
    // `needsResult` answers a boolean: no property of its answer can be excess.
    expect(offencesOf('  \'edit\': { needsResult: r => !r.path, build: (f): P => ({ kind: \'edit\' }) },\n')).toEqual([])
    // A local the function READS rather than hands back. `String(seen)` answers a
    // string, so no key the literal holds reaches the model through it.
    expect(offencesOf(
      '  \'fetch\': (args): Req => {\n'
      + '    const seen = { url: args.url }\n'
      + '    return { kind: \'fetch\', request: { url: String(seen) } }\n'
      + '  },\n',
    )).toEqual([])
    // A mapped table over another union is not a tool-call table.
    expect(scanTables('sample.ts', 'const C: { [K in Colour]: Draw<K> } = {\n  red: c => ({ hex: c }),\n}\n')).toEqual([])
  })

  it('reads the two declaration forms a table takes, and no other one', () => {
    expect(scanTables('sample.ts', 'const T: { [P in ToolKind]: R<P> } = {\n  read: a => ({ p: a }),\n}\n').map(t => t.name)).toEqual(['T'])
    expect(scanTables('sample.ts', 'const O: ToolRequestOverrides<F> = {\n  read: a => ({ p: a }),\n}\n').map(t => t.name)).toEqual(['O'])
    // A plain record of the same entries states no key set, so it is not a table here.
    expect(scanTables('sample.ts', 'const M: Record<string, R> = {\n  read: a => ({ p: a }),\n}\n')).toEqual([])
  })
})
