import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import ts from 'typescript'
import { describe, expect, it } from 'vitest'
import { collectFiles, frontendRoot, posixRelative } from '~/test-support/sourceTree'

// A tool name, a permission kind, a notification type and a decision word all arrive as
// arbitrary STRINGS off the wire, and the chat layer turns each into something a row
// draws by looking it up in a table. Every object in JavaScript inherits
// `Object.prototype`, so a lookup by a wire word answers for a key nobody put in the
// table: `TABLE['toString']` is a function, `TABLE['constructor']` is a constructor, and
// both are truthy.
//
// The consequence is not a wrong label. `?? fallback` and `|| fallback` never run for a
// truthy answer, so the fallback the author wrote is unreachable, and the caller then
// treats a function as the value. A stored decision word of `toString` gave
// `option.kind` the function, `option.kind.startsWith` was undefined, and the whole
// message went to the error boundary.
//
// WHICH TABLES THIS RULE READS, and why it is exactly these. A table whose type states a
// CLOSED key set is already safe, because the compiler refuses a `string` index into it:
// an `as const` object, a mapped type over a union, `Record<ToolKind, T>`. A
// `ReadonlyMap` is safe too, because `.get()` answers `T | undefined` and consults no
// prototype. One shape is neither, and it is the shape this guard reads: a declared
// STRING index signature -- `Record<string, T>`, `Partial<Record<string, T>>`,
// `{ [key: string]: T }` -- over an object LITERAL. That type compiles a `string` index
// and hands back `T`, so nothing reports the prototype hit. `Partial` is no escape: it
// widens the answer to `T | undefined` for an absent key, and an INHERITED key is not
// absent.
//
// THE THREE SAFE FORMS, all of which this guard leaves alone. Each is correct, each
// says so at its own table, and the rule pushes toward none of them in particular:
//
//   - `Object.hasOwn(TABLE, key)` before the read (copilot's `permissionOptions`,
//     kilo's `toolKinds`). The guard accepts a read whose enclosing function holds one
//     for the SAME table.
//   - A `ReadonlyMap` with `.get(key) ?? fallback` (claude, pi and zcode's tool kinds).
//     No object literal, so this guard never sees it.
//   - `as const satisfies Record<string, T>` plus a type predicate
//     (`isGooseDeveloperTool`, `isReasonixTool`). The `as const` keeps the key set
//     closed, so `tsc` itself refuses an unguarded `string` index and the predicate is
//     the only way in.
//
// THE FOURTH FORM IS THE BEST ONE, and it needs no guard at all: compose a key that no
// wire word can spell. `elicitationFieldKey` answers `` `elicitation:${JSON.stringify(key)}` ``,
// so every stored form key carries a prefix and quotation marks and no input can ever
// reach an `Object.prototype` member. Prefer that where you own the key format: it makes
// the mistake mechanically impossible rather than merely absent today.
//
// A PARSE rather than a text scan, for the reason `toolTableEntriesAreAnnotated`
// gives. The rule turns on the DECLARED TYPE of a module-level const, on whether the
// initializer is an object literal, and on whether a call sits in the enclosing
// function -- three facts no regular expression can read, and a scan of `TABLE[` alone
// would report every safe closed-set table in the tree. Parsing also removes the
// comment-stripping step the text guards beside this one need, because a comment is not
// a node.
//
// WHAT IT DOES NOT CATCH:
//
//   - A table declared OUTSIDE `src/components/chat`. A generated contract or a `~/lib`
//     table read from here is not in the walk.
//   - A string index signature that arrives through a type ALIAS
//     (`type Labels = Record<string, string>`). The rule reads the type node at the
//     declaration and resolves no alias.
//   - A read through a second name (`const table = TABLE; table[key]`), and a read on a
//     table a function BUILDS rather than one an object literal states.
//   - A guard that sits in the CALLER. `Object.hasOwn` must be in the same function as
//     the read, which is where every site in this tree puts it.
//   - The other prototype-walking reads: `key in TABLE` is true for an inherited key,
//     and so is `TABLE[key] !== undefined`. This rule reads the element access alone.

const CHAT_DIR = join(frontendRoot, 'src/components/chat')

/**
 * The string-keyed tables that exist today, pinned so a rename cannot make this vacuous.
 *
 * Discovery is a PATTERN and not this list, so a table a future provider adds is covered
 * with no edit here. The list exists for the opposite failure: a declaration form that
 * stops matching reports nothing, which reads exactly like a clean tree. A name here
 * that the walk no longer finds fails the case below and states which table went unread.
 */
const KNOWN_STRING_KEYED_TABLES = [
  'AGENT_STATES',
  'AGENT_TYPES',
  'CLAUDE_CONTENT_CLASSIFIERS',
  'CLAUDE_NOTIFICATION_CLASSIFIERS',
  'CODEX_APPROVAL_TITLES',
  'CODEX_ITEM_CLASSIFIERS',
  'CODEX_RATE_LIMIT_REACHED_LABELS',
  'CODEX_STATUS_ITEM_TITLES',
  'COPILOT_TOOL_KINDS',
  'DECISION_KINDS',
  'DECISION_OPTION_IDS',
  'GOAL_TRANSITION_VERBS',
  'KILO_TOOL_KINDS',
  'KIND_FALLBACK_LABELS',
  'OPTION_ID_KINDS',
  'STATUS_NOTES',
  'TOOL_LABELS',
]

/** One unguarded read, as the report states it. */
interface Offence {
  table: string
  at: string
  text: string
}

/** The implementation modules a guard reads: never a test, a fixture or a harness. */
function chatModules(): string[] {
  return collectFiles(CHAT_DIR, {
    matches: name => (name.endsWith('.ts') || name.endsWith('.tsx'))
      && !name.endsWith('.test.ts')
      && !name.endsWith('.test.tsx')
      && !name.endsWith('.css.ts')
      // Test DATA co-located with the module it describes; see `providerLayering.test.ts`.
      && !name.endsWith('.fixtures.ts')
      && name !== 'testUtils.ts'
      && name !== 'testUtils.tsx'
      && name !== 'testMocks.ts',
  })
}

function parse(path: string, text: string): ts.SourceFile {
  return ts.createSourceFile(path, text, ts.ScriptTarget.Latest, true)
}

/** `(expr)`, `expr as T` and `expr satisfies T` down to the expression they wrap. */
function unwrap(node: ts.Expression): ts.Expression {
  if (ts.isParenthesizedExpression(node) || ts.isAsExpression(node) || ts.isSatisfiesExpression(node))
    return unwrap(node.expression)
  return node
}

/**
 * Whether a type node admits an ARBITRARY string key.
 *
 * Three spellings, and a `Partial` or a `Readonly` around any of them. `Record<K, V>`
 * counts only when `K` is `string` itself: `Record<ToolKind, V>` states a closed set,
 * and the compiler already refuses a wire word there.
 */
function admitsAnyStringKey(node: ts.TypeNode | undefined): boolean {
  if (!node)
    return false
  // `{ [key: string]: T }`, written out.
  if (ts.isTypeLiteralNode(node)) {
    return node.members.some(member => ts.isIndexSignatureDeclaration(member)
      && member.parameters[0]?.type?.kind === ts.SyntaxKind.StringKeyword)
  }
  if (!ts.isTypeReferenceNode(node) || !ts.isIdentifier(node.typeName))
    return false
  const name = node.typeName.text
  const args = node.typeArguments ?? []
  // `inner` is present whenever the arity bounds below hold; the undefined check is the type-level guard alone.
  const inner = args[0]
  if ((name === 'Partial' || name === 'Readonly' || name === 'Required') && args.length === 1 && inner !== undefined)
    return admitsAnyStringKey(inner)
  return name === 'Record' && args.length === 2 && inner !== undefined && inner.kind === ts.SyntaxKind.StringKeyword
}

/** The module-level object-literal tables of one file whose type admits any string key. */
function stringKeyedTables(source: ts.SourceFile): Set<string> {
  const found = new Set<string>()
  for (const statement of source.statements) {
    if (!ts.isVariableStatement(statement))
      continue
    for (const declaration of statement.declarationList.declarations) {
      if (ts.isIdentifier(declaration.name) && declaration.initializer
        && ts.isObjectLiteralExpression(unwrap(declaration.initializer))
        && admitsAnyStringKey(declaration.type)) {
        found.add(declaration.name.text)
      }
    }
  }
  return found
}

/** A key the author WROTE, which no wire word can change. */
function isFixedKey(expr: ts.Expression): boolean {
  return ts.isStringLiteral(expr)
    || ts.isNumericLiteral(expr)
    || (ts.isNoSubstitutionTemplateLiteral(expr))
}

/** The function, method or arrow that encloses one node, or the source file at the top. */
function enclosingBody(node: ts.Node): ts.Node {
  for (let current = node.parent; current; current = current.parent) {
    if (ts.isFunctionDeclaration(current) || ts.isFunctionExpression(current) || ts.isArrowFunction(current)
      || ts.isMethodDeclaration(current) || ts.isSourceFile(current)) {
      return current
    }
  }
  return node
}

/** Whether one scope holds an `Object.hasOwn(<table>, ...)` call for this table. */
function guardsTable(scope: ts.Node, table: string): boolean {
  let found = false
  const visit = (node: ts.Node): void => {
    if (found)
      return
    if (ts.isCallExpression(node) && ts.isPropertyAccessExpression(node.expression)
      && ts.isIdentifier(node.expression.expression) && node.expression.expression.text === 'Object'
      && node.expression.name.text === 'hasOwn') {
      const target = node.arguments[0]
      // A `hasOwn` call states its first argument; the undefined check is the type-level guard alone.
      if (target !== undefined && ts.isIdentifier(target) && target.text === table) {
        found = true
        return
      }
    }
    ts.forEachChild(node, visit)
  }
  ts.forEachChild(scope, visit)
  return found
}

/** Every unguarded wire-keyed read one source carries. */
function unguardedReads(relative: string, text: string): Offence[] {
  const source = parse(relative, text)
  const tables = stringKeyedTables(source)
  if (tables.size === 0)
    return []
  const offences: Offence[] = []
  const visit = (node: ts.Node): void => {
    if (ts.isElementAccessExpression(node) && ts.isIdentifier(node.expression) && tables.has(node.expression.text)
      && !isFixedKey(node.argumentExpression)
      && !guardsTable(enclosingBody(node), node.expression.text)) {
      const line = source.getLineAndCharacterOfPosition(node.getStart(source)).line + 1
      offences.push({ table: node.expression.text, at: `${relative}:${line}`, text: node.getText(source).replace(/\s+/g, ' ') })
    }
    ts.forEachChild(node, visit)
  }
  ts.forEachChild(source, visit)
  return offences
}

/** Every string-keyed table the walk finds, and every unguarded read of one. */
function scanChatLayer(): { tables: string[], offences: Offence[] } {
  const tables: string[] = []
  const offences: Offence[] = []
  for (const file of chatModules()) {
    const relative = posixRelative(frontendRoot, file)
    const text = readFileSync(file, 'utf8')
    tables.push(...stringKeyedTables(parse(relative, text)))
    offences.push(...unguardedReads(relative, text))
  }
  return { tables, offences }
}

// The samples below pin the matcher itself. A guard whose matcher stops matching reads
// exactly like a clean tree -- green either way -- so each form it must report and each
// form it must leave alone is stated here as source it parses.

function offencesOf(body: string): Offence[] {
  return unguardedReads('sample.ts', body)
}

describe('a wire-keyed lookup is guarded', () => {
  it('finds every string-keyed table it guards', () => {
    const { tables } = scanChatLayer()
    expect(
      KNOWN_STRING_KEYED_TABLES.filter(name => !tables.includes(name)),
      'The walk stopped finding these tables, so nothing checked how they are read. Repoint '
      + '`admitsAnyStringKey`, or drop the name if the table is gone or now states a closed key set.',
    ).toStrictEqual([])
  })

  it('reads a bare index on a table that admits any string key', () => {
    expect(offencesOf('const T: Record<string, string> = { a: \'1\' }\nexport function read(k: string) { return T[k] }\n')).toHaveLength(1)
  })

  // `Partial` is no escape. It widens the answer to `T | undefined` for an ABSENT key,
  // and an inherited key is not absent: `T['toString']` is still the function.
  it('reads a bare index on a partial record and on a written-out index signature', () => {
    expect(offencesOf('const T: Partial<Record<string, string>> = { a: \'1\' }\nexport function read(k: string) { return T[k] ?? \'\' }\n')).toHaveLength(1)
    expect(offencesOf('const T: { [key: string]: string } = { a: \'1\' }\nexport function read(k: string) { return T[k] }\n')).toHaveLength(1)
    expect(offencesOf('const T: Readonly<Record<string, string>> = { a: \'1\' }\nexport function read(k: string) { return T[k] }\n')).toHaveLength(1)
  })

  it('reads a bare index whose key is a property of a wire object', () => {
    expect(offencesOf('const T: Record<string, string> = { a: \'1\' }\nexport function read(o: { kind: string }) { return T[o.kind] }\n')).toHaveLength(1)
  })

  it('reads every unguarded index of one table, not only the first', () => {
    expect(offencesOf('const T: Record<string, string> = { a: \'1\' }\nexport function read(j: string, k: string) { return T[j] + T[k] }\n')).toHaveLength(2)
  })

  // The three disciplines the tree already uses. None of them is this rule's business,
  // and each says so at its own table.
  it('reads no index that Object.hasOwn guards, in either shape', () => {
    expect(offencesOf('const T: Record<string, string> = { a: \'1\' }\nexport function read(k: string) { return Object.hasOwn(T, k) ? T[k] : \'\' }\n')).toStrictEqual([])
    expect(offencesOf('const T: Record<string, string> = { a: \'1\' }\nexport function read(k: string) { if (!Object.hasOwn(T, k)) return \'\'\n  return T[k] }\n')).toStrictEqual([])
  })

  it('reads no table whose type states a closed key set', () => {
    expect(offencesOf('const T = { a: \'1\' } as const\nexport function read(k: Key) { return T[k] }\n')).toStrictEqual([])
    expect(offencesOf('const T: Record<ToolKind, string> = { a: \'1\' }\nexport function read(k: ToolKind) { return T[k] }\n')).toStrictEqual([])
    expect(offencesOf('const T: { [P in ToolKind]: string } = { a: \'1\' }\nexport function read(k: ToolKind) { return T[k] }\n')).toStrictEqual([])
    expect(offencesOf('const T = { a: \'1\' } as const satisfies Record<string, string>\nexport function read(k: Key) { return T[k] }\n')).toStrictEqual([])
  })

  it('reads no map, because `get` consults no prototype', () => {
    expect(offencesOf('const T: ReadonlyMap<string, string> = new Map([[\'a\', \'1\']])\nexport function read(k: string) { return T.get(k) ?? \'\' }\n')).toStrictEqual([])
  })

  it('reads no key the author wrote', () => {
    expect(offencesOf('const T: Record<string, string> = { a: \'1\' }\nexport function read() { return T[\'a\'] + T[`b`] }\n')).toStrictEqual([])
  })

  // The guard must belong to the table that is read. `Object.hasOwn` on a SIBLING table
  // proves nothing about this one, and two tables beside each other is the exact shape
  // `permissionOptions` holds.
  it('reads an index whose only guard tests another table', () => {
    expect(offencesOf(
      'const T: Record<string, string> = { a: \'1\' }\n'
      + 'const U: Record<string, string> = { a: \'1\' }\n'
      + 'export function read(k: string) { return Object.hasOwn(U, k) ? T[k] : \'\' }\n',
    )).toHaveLength(1)
  })

  it('guards every wire-keyed lookup in the chat layer', () => {
    const { offences } = scanChatLayer()
    expect(
      offences.map(offence => `${offence.at} reads ${offence.text}`),
      'This table admits any string key, so a wire word that spells an `Object.prototype` member '
      + 'answers with the inherited value -- a truthy one, which makes the `?? fallback` beside it '
      + 'unreachable and hands a function to whatever reads the result. Take one of the three '
      + 'forms the tree already uses: `Object.hasOwn(TABLE, key)` before the read, a `ReadonlyMap` '
      + 'with `.get(key) ?? fallback`, or `as const satisfies` plus a type predicate. Better still, '
      + 'compose a key no wire word can spell, the way `elicitationFieldKey` does.',
    ).toStrictEqual([])
  })
})
