import type * as TypeScript from 'typescript'
import { dirname, resolve } from 'node:path'
import {
  createSourceFile,
  forEachChild,
  isArrowFunction,
  isCallExpression,
  isFunctionDeclaration,
  isFunctionExpression,
  isFunctionLike,
  isIdentifier,
  isImportDeclaration,
  isNamedImports,
  isPropertyAccessExpression,
  isStringLiteral,
  isThrowStatement,
  isTryStatement,
  isVariableDeclaration,
  ScriptTarget,
} from 'typescript'

// The analysis behind the E2E guard that refuses a Worker or Hub read inside `expect.poll`.
//
// Playwright calls the poll function outside the `try` that retries a failed matcher (`invokePollMatcher` in
// `playwright/lib/matchers/expect.js`). A poll function that throws therefore ends the poll at once, and only a failed
// matcher starts the next attempt. A Worker or Hub read throws while the Worker reconnects, so a poll over such a read
// fails a test that a later read would pass. `retryUntilPass` in `tests/e2e/helpers/retryUntilPass.ts` retries the
// read.
//
// The analysis follows calls by name through the E2E tree: a call to a function of the same file, or to a function that
// the file imports through a relative path. A read that reaches one of the transport calls below can throw. A call in
// the `try` block of a `catch` that throws nothing, or a call whose promise ends in `.catch(...)`, cannot.
//
// Separate from the guard, as `testFileNaming.ts` is: a guard over the real tree asserts an EMPTY list, and an analysis
// that finds nothing passes it forever. These functions take sources and return findings, so a case states what they
// must find.
//
// NOT a `.test.ts`, so vitest does not collect this module as a suite of its own. Its cases live in
// `throwingPollReads.test.ts` beside it.

/**
 * The functions whose call reaches the Hub, or the Worker through the Hub. Each throws when its request fails.
 * `runHubSql` reads the database of the Hub, which the Hub can hold locked longer than the function retries.
 */
export const TRANSPORT_FUNCTIONS: ReadonlySet<string> = new Set(['getTestChannel', 'callHub', 'hubRequest', 'runHubSql'])

/** The methods whose call reaches the Worker through an encrypted channel. Each throws when the channel fails. */
export const TRANSPORT_METHODS: ReadonlySet<string> = new Set(['callWorker'])

/** One source file of the analysis. */
export interface PollSourceFile {
  /** The absolute path. A relative import resolves against it. */
  path: string
  source: string
}

/** One `expect.poll` whose poll function can throw on a failed Worker or Hub request. */
export interface ThrowingPoll {
  path: string
  /** The 1-based line of the `expect.poll` call. */
  line: number
  /** The name of the function that holds the poll, or '' at the top level of the file. */
  enclosingFunction: string
  /** The calls from the poll function to the transport call, outermost first. */
  chain: string[]
}

interface ParsedFile {
  path: string
  file: TypeScript.SourceFile
  /** The bodies of the functions of the file, by name. One name can hold several functions. */
  functions: Map<string, TypeScript.Node[]>
  /** The imported names of the file, by local name, with the path of the module that exports each. */
  imports: Map<string, { path: string, name: string }>
}

/** Resolve a relative import of `fromPath` to a path of the analysis, or undefined when the analysis holds none. */
function resolveImport(fromPath: string, specifier: string, known: ReadonlySet<string>): string | undefined {
  if (!specifier.startsWith('.'))
    return undefined
  const base = resolve(dirname(fromPath), specifier)
  return [base, `${base}.ts`, `${base}/index.ts`].find(candidate => known.has(candidate))
}

function parse(input: PollSourceFile, known: ReadonlySet<string>): ParsedFile {
  const file = createSourceFile(input.path, input.source, ScriptTarget.Latest, /* setParentNodes */ true)
  const functions = new Map<string, TypeScript.Node[]>()
  const imports = new Map<string, { path: string, name: string }>()
  const addFunction = (name: string, body: TypeScript.Node | undefined) => {
    if (body)
      functions.set(name, [...(functions.get(name) ?? []), body])
  }
  const visit = (node: TypeScript.Node): void => {
    if (isFunctionDeclaration(node) && node.name) {
      addFunction(node.name.text, node.body)
    }
    else if (isVariableDeclaration(node) && isIdentifier(node.name) && node.initializer
      && (isArrowFunction(node.initializer) || isFunctionExpression(node.initializer))) {
      addFunction(node.name.text, node.initializer.body)
    }
    else if (isImportDeclaration(node) && isStringLiteral(node.moduleSpecifier) && node.importClause?.namedBindings
      && isNamedImports(node.importClause.namedBindings)) {
      const path = resolveImport(input.path, node.moduleSpecifier.text, known)
      if (path) {
        for (const element of node.importClause.namedBindings.elements)
          imports.set(element.name.text, { path, name: (element.propertyName ?? element.name).text })
      }
    }
    forEachChild(node, visit)
  }
  visit(file)
  return { path: input.path, file, functions, imports }
}

/** Whether a `catch` clause holds a `throw` statement of its own, outside a nested function. */
function catchThrows(clause: TypeScript.CatchClause): boolean {
  let found = false
  const visit = (node: TypeScript.Node): void => {
    if (found || (node !== clause && isFunctionLike(node)))
      return
    if (isThrowStatement(node))
      found = true
    else
      forEachChild(node, visit)
  }
  visit(clause)
  return found
}

/**
 * Whether `node` sits in the `try` block of a statement whose `catch` throws nothing, inside `boundary`.
 * Such a `catch` turns a failed read into a value, so the read cannot end the poll.
 */
function caughtWithin(node: TypeScript.Node, boundary: TypeScript.Node): boolean {
  for (let child: TypeScript.Node = node; child !== boundary && child.parent; child = child.parent) {
    const parent = child.parent
    if (isTryStatement(parent) && parent.tryBlock === child && parent.catchClause && !catchThrows(parent.catchClause))
      return true
  }
  return false
}

/** Whether the promise of `call` ends in a `.catch(...)` call, which turns a rejection into a value. */
function caughtByPromise(call: TypeScript.CallExpression): boolean {
  const parent = call.parent
  return isPropertyAccessExpression(parent) && parent.expression === call && parent.name.text === 'catch'
    && isCallExpression(parent.parent) && parent.parent.expression === parent
}

/** Return the 1-based line of `node`. */
function lineOf(file: TypeScript.SourceFile, node: TypeScript.Node): number {
  return file.getLineAndCharacterOfPosition(node.getStart(file)).line + 1
}

/** Return the name of the function that holds `node`, or '' at the top level. */
function enclosingFunctionName(node: TypeScript.Node): string {
  for (let parent = node.parent; parent; parent = parent.parent) {
    if (isFunctionDeclaration(parent) && parent.name)
      return parent.name.text
    if ((isArrowFunction(parent) || isFunctionExpression(parent)) && isVariableDeclaration(parent.parent) && isIdentifier(parent.parent.name))
      return parent.parent.name.text
  }
  return ''
}

/** Find each `expect.poll` of `inputs` whose poll function can throw on a failed Worker or Hub request. */
export function throwingPollReads(inputs: readonly PollSourceFile[]): ThrowingPoll[] {
  const known = new Set(inputs.map(input => input.path))
  const files = new Map(inputs.map(input => [input.path, parse(input, known)]))
  // The chain from a named function to a transport call, or null when the function reaches none.
  const memo = new Map<string, string[] | null>()
  // The functions on the stack of the current search. A call back into one of them reads as no chain, and the search
  // that met it keeps no negative result, because the function on the stack can still reach a transport call.
  const searching = new Set<string>()
  let metSearching = false

  function nameChain(file: ParsedFile, name: string): string[] | null {
    if (TRANSPORT_FUNCTIONS.has(name))
      return [name]
    const local = file.functions.get(name)
    if (!local) {
      const imported = file.imports.get(name)
      const target = imported ? files.get(imported.path) : undefined
      return imported && target ? nameChain(target, imported.name) : null
    }
    const key = `${file.path}#${name}`
    const known = memo.get(key)
    if (known !== undefined)
      return known
    if (searching.has(key)) {
      metSearching = true
      return null
    }
    searching.add(key)
    const outerMet = metSearching
    metSearching = false
    let chain: string[] | null = null
    for (const body of local) {
      const inner = bodyChain(file, body)
      if (inner) {
        chain = [name, ...inner]
        break
      }
    }
    searching.delete(key)
    if (chain || !metSearching)
      memo.set(key, chain)
    metSearching = outerMet || metSearching
    return chain
  }

  function bodyChain(file: ParsedFile, body: TypeScript.Node): string[] | null {
    let chain: string[] | null = null
    const visit = (node: TypeScript.Node): void => {
      if (chain)
        return
      if (isCallExpression(node) && !caughtByPromise(node) && !caughtWithin(node, body)) {
        const callee = node.expression
        if (isPropertyAccessExpression(callee) && TRANSPORT_METHODS.has(callee.name.text))
          chain = [callee.name.text]
        else if (isIdentifier(callee))
          chain = nameChain(file, callee.text)
        if (chain)
          return
      }
      forEachChild(node, visit)
    }
    visit(body)
    return chain
  }

  const findings: ThrowingPoll[] = []
  for (const file of files.values()) {
    const visit = (node: TypeScript.Node): void => {
      if (isCallExpression(node) && isPropertyAccessExpression(node.expression) && node.expression.name.text === 'poll'
        && isIdentifier(node.expression.expression) && node.expression.expression.text === 'expect') {
        const poll = node.arguments[0]
        const chain = poll === undefined ? null : isIdentifier(poll) ? nameChain(file, poll.text) : bodyChain(file, poll)
        if (chain)
          findings.push({ path: file.path, line: lineOf(file.file, node), enclosingFunction: enclosingFunctionName(node), chain })
      }
      forEachChild(node, visit)
    }
    visit(file.file)
  }
  return findings
}
