import type * as TypeScript from 'typescript'
import { isArrowFunction, isFunctionDeclaration, isFunctionExpression, isIdentifier, isVariableDeclaration } from 'typescript'

// Where a node of a parsed source sits: its line, and the function that holds it.
//
// The E2E guards that keep a list of known sites identify a site by its file and its enclosing function, so a line
// shift does not break the entry:
//
// - `throwingPollReads.test.ts`.
// - `fixedWaits.test.ts`.
// - `platformModifierKeys.test.ts`.
//
// One definition of the enclosing function keeps the keys of every list the same.
//
// NOT a `.test.ts`, so vitest does not collect this module as a suite of its own. Its cases live in
// `syntaxSite.test.ts` beside it.

/** Return the 1-based line of `node`. */
export function lineOf(file: TypeScript.SourceFile, node: TypeScript.Node): number {
  return file.getLineAndCharacterOfPosition(node.getStart(file)).line + 1
}

/**
 * Return the name of the innermost named function that holds `node`, or '' when no named function holds it.
 *
 * A function has a name when it is a function declaration with a name, or a function that initializes a variable. An
 * anonymous function, such as a callback, gives no name, so the search continues outward from it.
 */
export function enclosingFunctionName(node: TypeScript.Node): string {
  for (let parent = node.parent; parent; parent = parent.parent) {
    if (isFunctionDeclaration(parent) && parent.name)
      return parent.name.text
    if ((isArrowFunction(parent) || isFunctionExpression(parent)) && isVariableDeclaration(parent.parent) && isIdentifier(parent.parent.name))
      return parent.parent.name.text
  }
  return ''
}
