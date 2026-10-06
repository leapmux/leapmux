import type * as TypeScript from 'typescript'
import {
  createSourceFile,
  forEachChild,
  isBindingElement,
  isElementAccessExpression,
  isIdentifier,
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
// Separate from the guard, as `throwingPollReads.ts` is: a guard over the real tree asserts an EMPTY list, and an
// analysis that finds nothing passes it forever. This function takes sources and returns findings, so a case states
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

/** One reference to the fixed-wait method. */
export interface FixedWait {
  path: string
  /** The 1-based line of the method name. */
  line: number
  /** The name of the function that holds the reference, or '' when no named function holds it. */
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
  const findings: FixedWait[] = []
  for (const input of inputs) {
    const file = createSourceFile(input.path, input.source, ScriptTarget.Latest, /* setParentNodes */ true)
    const visit = (node: TypeScript.Node): void => {
      const name = fixedWaitName(node)
      if (name)
        findings.push({ path: input.path, line: lineOf(file, name), enclosingFunction: enclosingFunctionName(node) })
      forEachChild(node, visit)
    }
    visit(file)
  }
  return findings
}
