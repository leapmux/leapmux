import type * as TypeScript from 'typescript'
import {
  createSourceFile,
  forEachChild,
  isArrowFunction,
  isBindingElement,
  isCallExpression,
  isElementAccessExpression,
  isFunctionExpression,
  isIdentifier,
  isNewExpression,
  isObjectBindingPattern,
  isPropertyAccessExpression,
  isStringLiteralLike,
  ScriptTarget,
} from 'typescript'
import { enclosingFunctionName, lineOf } from '~/test-support/syntaxSite'

// The analysis behind the E2E guard that refuses a fixed wait.
//
// `waitForTimeout` ends a window after a fixed wall-clock time. Under load, the state change that the window covers can
// come later, and the wait then ends too early. When nothing happens, the wait costs its full length. AGENTS.md
// requires the injected end of the transient state, or a proof that cannot pass early ("never size a window with a
// sleep"). `helpers/frames.ts` holds the waits that end at a rendered frame.
//
// The analysis reads the syntax tree, so the method name in a comment or in a plain string is not a finding. It finds
// each reference to the method as a member, called or not:
//
// - `page.waitForTimeout(ms)`, on any receiver.
// - `frame['waitForTimeout'](ms)`.
// - `page.waitForTimeout.bind(page)`.
// - `const { waitForTimeout } = page`.
//
// A sleep is the same fixed wait in another spelling, and a hand-written poll loop sleeps between its reads. The
// second analysis finds a sleep: a call of `sleep`, and a promise that `setTimeout` resolves. `retryUntilPass` in
// `tests/e2e/helpers/retryUntilPass.ts` is the poll loop that a spec uses instead.
//
// Separate from the guard, as `throwingPollReads.ts` is: a guard over the real tree asserts an EMPTY list, and an
// analysis that finds nothing passes it forever. These functions take sources and return findings, so a case states
// what it must find.
//
// NOT a `.test.ts`, so vitest does not collect this module as a suite of its own. Its cases live in
// `fixedWaits.test.ts` beside it.

/** The Playwright method that waits for a fixed time. */
export const FIXED_WAIT_METHOD = 'waitForTimeout'

/** One source file of the analysis. */
export interface WaitSourceFile {
  /** The absolute path. */
  path: string
  source: string
}

/** One fixed wait: a reference to the fixed-wait method, or a sleep. */
export interface FixedWait {
  path: string
  /** The 1-based line of the name that spells the wait: the method, `sleep`, or `setTimeout`. */
  line: number
  /** The name of the function that holds the wait, or '' when no named function holds it. */
  enclosingFunction: string
}

/** Return the node that spells the method name when `node` refers to the fixed-wait method as a member, or undefined. */
function fixedWaitName(node: TypeScript.Node): TypeScript.Node | undefined {
  if (isPropertyAccessExpression(node))
    return node.name.text === FIXED_WAIT_METHOD ? node.name : undefined
  if (isElementAccessExpression(node)) {
    const key = node.argumentExpression
    return isStringLiteralLike(key) && key.text === FIXED_WAIT_METHOD ? key : undefined
  }
  if (isBindingElement(node) && isObjectBindingPattern(node.parent)) {
    const property = node.propertyName ?? node.name
    return (isIdentifier(property) || isStringLiteralLike(property)) && property.text === FIXED_WAIT_METHOD ? property : undefined
  }
  return undefined
}

/** Find each reference to the fixed-wait method in `inputs`. */
export function fixedWaits(inputs: readonly WaitSourceFile[]): FixedWait[] {
  return findInSources(inputs, fixedWaitName)
}

/** The function that a test imports from `src/lib/sleep.ts` to wait for a fixed time. */
export const SLEEP_FUNCTION = 'sleep'

/** Return whether `node` is the resolver of the `new Promise` whose executor holds it: the first parameter. */
function isPromiseResolver(node: TypeScript.Node): boolean {
  if (!isIdentifier(node))
    return false
  for (let parent = node.parent; parent; parent = parent.parent) {
    if (!isArrowFunction(parent) && !isFunctionExpression(parent))
      continue
    const executor = parent
    const resolver = executor.parameters[0]?.name
    if (!resolver || !isIdentifier(resolver) || resolver.text !== node.text)
      continue
    const owner = executor.parent
    return isNewExpression(owner) && isIdentifier(owner.expression) && owner.expression.text === 'Promise' && owner.arguments?.[0] === executor
  }
  return false
}

/** Return whether `node` calls the timer function `setTimeout`, on the global scope or on a named object such as `window`. */
function isSetTimeoutCall(node: TypeScript.CallExpression): boolean {
  const callee = node.expression
  if (isIdentifier(callee))
    return callee.text === 'setTimeout'
  return isPropertyAccessExpression(callee) && callee.name.text === 'setTimeout'
}

/** Return whether `callback` of a timer resolves a promise: the resolver itself, or an arrow that only calls it. */
function resolvesPromise(callback: TypeScript.Node): boolean {
  if (isPromiseResolver(callback))
    return true
  return isArrowFunction(callback) && isCallExpression(callback.body) && isPromiseResolver(callback.body.expression)
}

/**
 * Return the node that spells a sleep when `node` is one, or undefined.
 *
 * - A call of `sleep(ms)`.
 * - `new Promise(resolve => setTimeout(resolve, ms))`, also with an arrow that only calls the resolver.
 */
function fixedSleepName(node: TypeScript.Node): TypeScript.Node | undefined {
  if (!isCallExpression(node))
    return undefined
  if (isIdentifier(node.expression) && node.expression.text === SLEEP_FUNCTION)
    return node.expression
  const callback = node.arguments[0]
  return isSetTimeoutCall(node) && callback !== undefined && resolvesPromise(callback) ? node.expression : undefined
}

/**
 * Find each sleep in `inputs`: a call of `sleep`, or a promise that a timer resolves.
 * A sleep in a spec is a fixed wait in another spelling, so the guard refuses it in a spec. A helper keeps the sleeps
 * that implement a wait, such as the pause between two attempts of `retryUntilPass`.
 */
export function fixedSleeps(inputs: readonly WaitSourceFile[]): FixedWait[] {
  return findInSources(inputs, fixedSleepName)
}

/** Parse each source, and report each node for which `spelling` returns the node that spells the finding. */
function findInSources(inputs: readonly WaitSourceFile[], spelling: (node: TypeScript.Node) => TypeScript.Node | undefined): FixedWait[] {
  const findings: FixedWait[] = []
  for (const input of inputs) {
    const file = createSourceFile(input.path, input.source, ScriptTarget.Latest, /* setParentNodes */ true)
    const visit = (node: TypeScript.Node): void => {
      const name = spelling(node)
      if (name)
        findings.push({ path: input.path, line: lineOf(file, name), enclosingFunction: enclosingFunctionName(node) })
      forEachChild(node, visit)
    }
    visit(file)
  }
  return findings
}
