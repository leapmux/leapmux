import type * as TypeScript from 'typescript'
import {
  createSourceFile,
  forEachChild,
  isCallExpression,
  isIdentifier,
  isPropertyAccessExpression,
  isStringLiteralLike,
  ScriptTarget,
} from 'typescript'
import { enclosingFunctionName, lineOf } from '~/test-support/syntaxSite'

// The analysis behind the guard that refuses an assertion that any error satisfies.
//
// `expect(fn).toThrow()` passes for EVERY error. When the code under test fails for a different reason -- a null
// dereference, a mock that a test forgot, a wrong fixture -- the test still passes, and it no longer tests the reason
// that its title states. A specific assertion states the reason:
//
// - A message substring or a regular expression: `toThrow('tokenizer exceeded limit')`, `toThrow(/^Unknown key/)`.
// - The error class, when the class is the reason: `toThrow(WorkerRpcError)`.
// - The fields of the error: `rejects.toMatchObject({ code: 'ENOENT' })`.
//
// The analysis reads the syntax tree, so a matcher name in a comment or in a plain string is not a finding. It finds
// each assertion that any error satisfies:
//
// - `toThrow` or `toThrowError` with no argument, or with an argument that any error matches: `undefined`, the empty
//   string, `Error`, `expect.any(Error)`, and `expect.anything()`. A matcher on `.rejects` counts the same.
// - A matcher on `.rejects` that any rejection satisfies: `toBeDefined()`, `toBeTruthy()`, `toBeInstanceOf(Error)`,
//   and `toEqual` or `toStrictEqual` with `expect.any(Error)` or `expect.anything()`.
//
// A negated matcher (`.not.toThrow()`) is not a finding: "throws nothing" is a specific assertion.
//
// Separate from the guard, as `fixedWaits.ts` is: a guard over the real tree asserts an EMPTY list, and an analysis
// that finds nothing passes it forever. This function takes sources and returns findings, so a case states what it
// must find.
//
// NOT a `.test.ts`, so vitest does not collect this module as a suite of its own. Its cases live in
// `anyErrorAssertions.test.ts` beside it.

/** The matchers that assert a thrown error or a rejection reason, and that take the expected error as an argument. */
const THROW_MATCHERS: ReadonlySet<string> = new Set(['toThrow', 'toThrowError'])

/** The functions that open a suite or a test, and whose first argument is the title. */
const TEST_FUNCTIONS: ReadonlySet<string> = new Set(['describe', 'it', 'test', 'suite'])

/** The separator between the titles of a suite and the test inside it, in `AnyErrorAssertion.site`. */
const TITLE_SEPARATOR = ' > '

/** One source file of the analysis. */
export interface AssertionSourceFile {
  /** The absolute path. */
  path: string
  source: string
}

/** One assertion that any error satisfies. */
export interface AnyErrorAssertion {
  path: string
  /** The 1-based line of the matcher name. */
  line: number
  /** The matcher as the source spells it, with a leading `rejects.` on a rejection: `toThrow()`, `rejects.toBeDefined()`. */
  matcher: string
  /**
   * Where the assertion sits. Inside a suite or a test, the titles of each `describe` and `it` that hold it, outermost
   * first and joined by `TITLE_SEPARATOR`. Outside every suite, the name of the enclosing function, or '' when no named
   * function holds it.
   */
  site: string
}

/** Return whether `node` calls `expect.<name>` with the arguments that `matches` accepts. */
function isExpectCall(node: TypeScript.Expression, name: string, matches: (args: TypeScript.NodeArray<TypeScript.Expression>) => boolean): boolean {
  return isCallExpression(node)
    && isPropertyAccessExpression(node.expression)
    && isIdentifier(node.expression.expression)
    && node.expression.expression.text === 'expect'
    && node.expression.name.text === name
    && matches(node.arguments)
}

/** Return whether `node` is the class `Error` itself, which every built-in error and every subclass satisfies. */
function isErrorClass(node: TypeScript.Expression | undefined): boolean {
  return node !== undefined && isIdentifier(node) && node.text === 'Error'
}

/** Return whether `node` is an asymmetric matcher that any error satisfies: `expect.any(Error)` or `expect.anything()`. */
function isAnyErrorMatcher(node: TypeScript.Expression | undefined): boolean {
  if (node === undefined)
    return false
  return isExpectCall(node, 'any', args => args.length === 1 && isErrorClass(args[0]))
    || isExpectCall(node, 'anything', args => args.length === 0)
}

/**
 * Return whether the argument of a throw matcher accepts any error. An absent argument does, and so do `undefined` and
 * the empty string, which every message contains.
 */
function isAnyErrorExpectation(node: TypeScript.Expression | undefined): boolean {
  if (node === undefined)
    return true
  if (isIdentifier(node) && node.text === 'undefined')
    return true
  if (isStringLiteralLike(node) && node.text === '')
    return true
  return isErrorClass(node) || isAnyErrorMatcher(node)
}

/** Return whether a matcher on `.rejects` with these arguments accepts any rejection reason that is an error. */
function acceptsAnyRejection(matcher: string, args: TypeScript.NodeArray<TypeScript.Expression>): boolean {
  switch (matcher) {
    case 'toBeDefined':
    case 'toBeTruthy':
      return args.length === 0
    case 'toBeInstanceOf':
      return args.length === 1 && isErrorClass(args[0])
    case 'toEqual':
    case 'toStrictEqual':
      return args.length === 1 && isAnyErrorMatcher(args[0])
    default:
      return false
  }
}

/** A matcher call that any error satisfies. */
interface AnyErrorMatcher {
  /** The node that spells the matcher name, for the line of the finding. */
  name: TypeScript.Node
  /** `AnyErrorAssertion.matcher`. */
  spelling: string
}

/** Return the matcher when `node` is an assertion that any error satisfies, or undefined. */
function anyErrorMatcher(node: TypeScript.Node): AnyErrorMatcher | undefined {
  if (!isCallExpression(node) || !isPropertyAccessExpression(node.expression))
    return undefined
  const { name, expression: receiver } = node.expression
  const receiverName = isPropertyAccessExpression(receiver) ? receiver.name.text : ''
  if (receiverName === 'not')
    return undefined
  const args = node.arguments
  const found = THROW_MATCHERS.has(name.text)
    ? args.length <= 1 && isAnyErrorExpectation(args[0])
    : receiverName === 'rejects' && acceptsAnyRejection(name.text, args)
  if (!found)
    return undefined
  const prefix = receiverName === 'rejects' ? 'rejects.' : ''
  return { name, spelling: `${prefix}${name.text}(${args.map(arg => arg.getText()).join(', ')})` }
}

/**
 * Return the identifier at the root of the callee of a suite or test call: `it` for `it(...)`, `it.only(...)`, and
 * `it.each(table)(...)`. Return '' for any other callee.
 */
function calleeRoot(callee: TypeScript.Expression): string {
  let node = callee
  while (isPropertyAccessExpression(node) || isCallExpression(node))
    node = node.expression
  return isIdentifier(node) ? node.text : ''
}

/** Return the title of a suite or test call: the text of a literal, or the source of any other expression. */
function titleOf(call: TypeScript.CallExpression): string {
  const title = call.arguments[0]
  if (title === undefined)
    return ''
  return isStringLiteralLike(title) ? title.text : title.getText()
}

/** Return the site of `node`, as `AnyErrorAssertion.site` defines it. */
function siteOf(node: TypeScript.Node): string {
  const titles: string[] = []
  for (let child = node, parent = node.parent; parent; child = parent, parent = parent.parent) {
    // The node must lie in an argument, the callback, not in the callee: `it.each(table)` holds the table in a callee.
    if (isCallExpression(parent) && child !== parent.expression && TEST_FUNCTIONS.has(calleeRoot(parent.expression)))
      titles.unshift(titleOf(parent))
  }
  return titles.length > 0 ? titles.join(TITLE_SEPARATOR) : enclosingFunctionName(node)
}

/**
 * Return whether `source` can hold a finding. Each finding spells a throw matcher, whose names all start with
 * `toThrow`, or a property named `rejects`. A source without either needs no parse, so the scan of the whole package
 * parses about one file in eight.
 */
function mayHoldFinding(source: string): boolean {
  return source.includes('toThrow') || source.includes('rejects')
}

/** Find each assertion that any error satisfies in `inputs`. */
export function anyErrorAssertions(inputs: readonly AssertionSourceFile[]): AnyErrorAssertion[] {
  const findings: AnyErrorAssertion[] = []
  for (const input of inputs) {
    if (!mayHoldFinding(input.source))
      continue
    const file = createSourceFile(input.path, input.source, ScriptTarget.Latest, /* setParentNodes */ true)
    const visit = (node: TypeScript.Node): void => {
      const matcher = anyErrorMatcher(node)
      if (matcher)
        findings.push({ path: input.path, line: lineOf(file, matcher.name), matcher: matcher.spelling, site: siteOf(node) })
      forEachChild(node, visit)
    }
    visit(file)
  }
  return findings
}
