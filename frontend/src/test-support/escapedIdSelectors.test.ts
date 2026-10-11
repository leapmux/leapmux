import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { collectE2EFiles } from '~/test-support/e2eFiles'
import { stripCommentLines } from '~/test-support/sourceScan'
import { frontendRoot, posixRelative } from '~/test-support/sourceTree'

// E2E guard: a value interpolated into the selector of an ID attribute goes through `cssAttributeValue`.
//
// An ID attribute (`data-tab-id`, `data-task-id`, `data-child-agent-id`, `data-tool-call-id`, and the rest) holds an ID
// that the Worker, the Hub, or a script chose. Such an ID can hold any character, and a Cursor call ID holds a line
// break. An unescaped quote ends the quoted CSS value early, and an unescaped backslash escapes the character after it,
// so the selector matches another element or no element. A line break makes the selector invalid. Then the test fails
// at a locator with a message about a missing element, or it passes on the wrong element.
//
// `tests/e2e/helpers/cssAttribute.ts` escapes one value. `tests/e2e/helpers/tabSelectors.ts` builds the escaped
// selector of a tab ID, which `tabById` uses.
//
// The guard reads the text of each spec and helper. A co-located unit test (`.test.ts`) is out of scope: it runs no
// page, and it spells the selector that it expects from the helper under test, with the value that the case already
// escaped.

/**
 * An interpolation in the quoted value of an ID attribute selector, unless `cssAttributeValue(` starts it.
 * The value can hold literal text before the interpolation, as in `[data-task-id="workflow:${id}"]`.
 */
const UNESCAPED_ID_INTERPOLATION = /\[data-[\w-]+-id[~|^$*]?="[^"]*\$\{(?!cssAttributeValue\()/

/** Return the 1-based number of each code line of `source` that interpolates an unescaped value into an ID selector. */
function unescapedIdSelectorLines(source: string): number[] {
  return stripCommentLines(source).split('\n').flatMap((text, index) => UNESCAPED_ID_INTERPOLATION.test(text) ? [index + 1] : [])
}

/**
 * A source line of a case, with `#{` in place of each `${` that opens an interpolation.
 * The cases spell source text, and a literal `${` in a plain string fails `no-template-curly-in-string`.
 */
function code(text: string): string {
  return text.replaceAll('#{', '$'.concat('{'))
}

describe('unescapedIdSelectorLines', () => {
  it.each([
    ['a plain interpolation', 'page.locator(`[data-tab-id="#{terminalId}"]:visible`)'],
    ['an interpolation after literal text', 'page.locator(`[data-task-id="workflow:#{launch.runId}"]`)'],
    ['an interpolation into an attribute name of several words', 'page.locator(`[data-child-agent-id="#{step.childAgentId}"]`)'],
    ['an unescaped interpolation after an escaped one', 'page.locator(`[data-task-id="#{cssAttributeValue(a)}"][data-child-agent-id="#{b}"]`)'],
    ['a second interpolation in an escaped value', 'page.locator(`[data-task-id="#{cssAttributeValue(a)}:#{b}"]`)'],
    ['a prefix match', 'page.locator(`[data-tool-call-id^="#{prefix}"]`)'],
  ])('finds %s', (_label, line) => {
    expect(unescapedIdSelectorLines(`const a = 1\n${code(line)}\n`)).toEqual([2])
  })

  it.each([
    ['an escaped value', 'page.locator(`[data-tab-id="#{cssAttributeValue(tabId)}"]`)'],
    ['an escaped value after literal text', 'page.locator(`[data-task-id="workflow:#{cssAttributeValue(id)}"]`)'],
    ['a literal value', 'page.locator(\'[data-tab-id="tab-1"]\')'],
    ['the test ID attribute, which is no ID attribute', 'page.locator(`[data-testid="#{testId}"]`)'],
    ['an attribute read', 'await row.getAttribute(\'data-child-agent-id\')'],
    ['an interpolation outside the attribute value', 'page.locator(`#{AGENT_TAB_SELECTOR}#{tabIdSelector(id)}:visible`)'],
    ['a comment line', '// page.locator(`[data-tab-id="#{terminalId}"]`)'],
  ])('accepts %s', (_label, line) => {
    expect(unescapedIdSelectorLines(`${code(line)}\n`)).toEqual([])
  })
})

describe('e2e ID attribute selectors', () => {
  it('escape every value that they interpolate', () => {
    const offenders: string[] = []
    for (const file of collectE2EFiles()) {
      if (file.endsWith('.test.ts'))
        continue
      const source = readFileSync(file, 'utf-8')
      const lines = source.split('\n')
      for (const line of unescapedIdSelectorLines(source))
        offenders.push(`${posixRelative(frontendRoot, file)}:${line}  ${lines[line - 1]?.trim() ?? ''}`)
    }
    // An empty walk passes this check with no offender. `e2eFiles.test.ts` pins that the walk finds files.
    expect(offenders, `Wrap each interpolated ID in cssAttributeValue, or use tabIdSelector for a tab ID:\n  ${offenders.join('\n  ')}`).toEqual([])
  })
})
