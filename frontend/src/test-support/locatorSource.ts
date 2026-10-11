// Shared primitives for the source guards of E2E helper modules:
// `tests/e2e/helpers/subagentRegistry.test.ts` and
// `tests/e2e/helpers/goalsAndTodos.test.ts` read the SOURCE of their module and
// check the scope of each locator, before a slow browser spec times out on a
// locator that matches a hidden duplicate.
//
// NOT a `.test.ts`, so vitest does not collect this module as a suite of its
// own. Its cases live in `locatorSource.test.ts` beside it.

/**
 * A `page.locator('<selector>')` call.
 *
 * Exclude only the enclosing quote through the lookahead. A selector can
 * contain a different quote, such as '[data-testid="x"]', and excluding every
 * quote stops the match at the inner quote and finds no complete selector.
 */
const LOCATOR = /page\.locator\(\s*(['"`])((?:(?!\1).)*)\1/g

/**
 * A `page.getByTestId('<id>')` call. A helper can select an element through
 * either form, so a guard checks both, or a new helper escapes it.
 */
const TEST_ID = /page\.getByTestId\(\s*(['"`])((?:(?!\1).)*)\1/g

/**
 * Every selector that `text` passes to `page.locator` or `page.getByTestId`, in
 * one form: a test ID becomes its `[data-testid="<id>"]` selector, so one
 * predicate checks both call forms.
 */
export function selectorsIn(text: string): string[] {
  // Group 2 always matches, including an empty value. The fallback satisfies the type checker.
  return [
    ...[...text.matchAll(LOCATOR)].map(match => match[2] ?? ''),
    ...[...text.matchAll(TEST_ID)].map(match => `[data-testid="${match[2] ?? ''}"]`),
  ]
}

/**
 * The source of one exported function of a module, from its signature to its
 * closing brace at the start of a line.
 *
 * Throw when the module no longer exports a function of that name, so a guard
 * that names a renamed helper fails instead of checking nothing.
 */
export function exportedFunctionBody(source: string, name: string, moduleName: string): string {
  const asyncStart = source.indexOf(`export async function ${name}(`)
  const start = asyncStart < 0 ? source.indexOf(`export function ${name}(`) : asyncStart
  const end = start < 0 ? -1 : source.indexOf('\n}\n', start)
  if (end < 0)
    throw new Error(`${name} is no longer an exported function of ${moduleName}`)
  return source.slice(start, end)
}
