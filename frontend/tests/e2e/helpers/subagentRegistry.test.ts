/**
 * The unit tests check the source rules for shared registry locators.
 * claude-code/background-tasks-sidebar.spec.ts checks actual Worker state before hydration.
 * Goal transition tests exercise the separate pure parser.
 *
 * The .test.ts extension selects Vitest. The .spec.ts extension selects Playwright.
 * Both runner configurations and testFileNaming.test.ts enforce that distinction.
 */
import type { Locator, Page } from '@playwright/test'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it, vi } from 'vitest'
import { openChildTabFromRow } from './subagentRegistry'

const source = readFileSync(join(import.meta.dirname, 'subagentRegistry.ts'), 'utf-8')

// Exclude only the enclosing quote through the lookahead.
// A selector can contain a different quote, such as '[data-testid="x"]'.
// Excluding every quote stops that match at the inner quote and finds no complete selector.
const LOCATOR = /page\.locator\(\s*(['"`])((?:(?!\1).)*)\1/g

// The helper can select an element through getByTestId also.
// Check both forms so a new helper cannot escape the source guard.
// A minimum total count cannot detect an unexamined locator family.
// Convert the captured test ID to its selector form so one predicate checks both forms.
const TEST_ID = /page\.getByTestId\(\s*(['"`])((?:(?!\1).)*)\1/g

function selectorsIn(text: string): string[] {
  // Group 2 always matches, including an empty value.
  // The fallback satisfies the type checker.
  return [
    ...[...text.matchAll(LOCATOR)].map(match => match[2] ?? ''),
    ...[...text.matchAll(TEST_ID)].map(match => `[data-testid="${match[2]}"]`),
  ]
}

/** The source of one exported helper, from its signature to its closing brace. */
function bodyOf(name: string): string {
  const asyncStart = source.indexOf(`export async function ${name}(`)
  const start = asyncStart < 0 ? source.indexOf(`export function ${name}(`) : asyncStart
  const end = start < 0 ? -1 : source.indexOf('\n}\n', start)
  if (end < 0)
    throw new Error(`${name} is no longer an exported function of subagentRegistry.ts`)
  return source.slice(start, end)
}

/**
 * Check the visible scope for shared locators.
 * ChatView can render a hidden premeasure copy of an unmeasured row.
 * The sidebar also has desktop and mobile mounts.
 * A bare test ID can match both copies and fail strict mode.
 * An unscoped first() can select a hidden copy and inspect state that the user cannot see.
 *
 * Present-element locators require :visible.
 * Sidebar section locators select the first visible mount that receives Worker metadata.
 * Absence locators require every copy to be absent, including hidden rows.
 * ABSENCE_HELPERS identifies that separate rule.
 * Source checks detect these defects before a slow browser spec times out.
 */
describe('registry locators', () => {
  it.each(['backgroundTasksSection', 'goalsAndTodosSection'])('selects the first visible sidebar mount in %s', (name) => {
    const body = bodyOf(name)
    expect(selectorsIn(body)).toHaveLength(1)
    expect(body).toMatch(/return page\.locator\([^\n]*:visible[^\n]*\)\.first\(\)/)
  })
  /** Test IDs for surfaces that the app mounts more than once. */
  const DUPLICATED = ['bg-task-', 'goal-', 'section-header-']

  /**
   * A zero-count assertion requires both mounts to contain no rows.
   * A :visible scope could hide rows in a collapsed section and falsely report zero.
   */
  const ABSENCE_HELPERS = ['expectNoRegistryRows']

  it('scopes every duplicated-surface locator to :visible', () => {
    const locators = selectorsIn(
      ABSENCE_HELPERS.reduce((rest, name) => rest.replace(bodyOf(name), ''), source),
    )
    // Require selectors so a parser or helper change cannot pass through an empty scan.
    expect(locators.length).toBeGreaterThan(5)

    const offenders = locators.filter(selector =>
      DUPLICATED.some(id => selector.includes(`data-testid="${id}`)) && !selector.includes(':visible'),
    )
    expect(offenders).toEqual([])
  })

  /**
   * Check both locator syntax forms.
   * A helper that uses getByTestId must obey the same element-scope rule as page.locator.
   * Use a fixture to test both parser paths because the production helper can use only one syntax form.
   * A minimum total selector count cannot prove that the parser supports both forms.
   */
  it('reads a getByTestId locator, not only a page.locator one', () => {
    const sample = `
      page.locator('[data-testid="goal-card"]:visible')
      page.getByTestId('agent-input-queue')
    `
    expect(selectorsIn(sample)).toEqual([
      '[data-testid="goal-card"]:visible',
      '[data-testid="agent-input-queue"]',
    ])
  })

  it('leaves an absence assertion unscoped, so a collapsed section cannot pass it', () => {
    for (const name of ABSENCE_HELPERS) {
      const locators = selectorsIn(bodyOf(name))
      expect(locators.length, `${name} should build at least one locator`).toBeGreaterThan(0)
      expect(locators.filter(selector => selector.includes(':visible'))).toEqual([])
    }
  })
})

interface NavigationState {
  ids: string[]
  selectedId: string
  childId: string | null
}

/**
 * Narrow adapters test the real navigation helper without a browser-install dependency.
 * The adapters provide only the methods that the helper calls.
 * Any new unsupported method fails the test rather than supplying a default result.
 */
function navigation(state: NavigationState, change: () => void) {
  const locator = (selector: string): Locator => {
    const selected = /\[data-tab-id="([^"\]]+)"\]/.exec(selector)?.[1]
    const ids = () => selected === undefined ? state.ids : state.ids.filter(id => id === selected)
    const attributes = (name: string) => name === 'data-tab-id'
      ? selected ?? ids()[0] ?? null
      : name === 'aria-selected' ? String(state.selectedId === (selected ?? ids()[0])) : null
    const probe = Object.assign({} as Locator, {
      readCount: () => ids().length,
      readAttribute: attributes,
      count: async () => ids().length,
      getAttribute: async (name: string) => attributes(name),
      evaluateAll: async (read: (elements: Element[]) => unknown) => read(ids().map((id) => {
        return Object.assign({} as Element, { getAttribute: (name: string) => name === 'data-tab-id' ? id : null })
      })),
      filter: () => probe,
      first: () => probe,
      isVisible: async () => ids().length > 0,
    })
    return probe
  }
  const page = Object.assign({} as Page, { locator })
  const click = vi.fn(async () => change())
  const row = Object.assign({} as Locator, { getAttribute: async () => state.childId, click })
  return { page, row, click }
}

vi.mock('@playwright/test', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@playwright/test')>()
  const check = (value: unknown, message?: string) => {
    if (typeof value === 'object' && value !== null && 'readCount' in value && typeof value.readCount === 'function'
      && 'readAttribute' in value && typeof value.readAttribute === 'function') {
      const count = value.readCount
      const attribute = value.readAttribute
      return {
        toHaveCount: async (expected: number) => expect(count(), message).toBe(expected),
        toBeVisible: async () => expect(count(), message).toBeGreaterThan(0),
        toHaveAttribute: async (key: string, expected: string) => expect(attribute(key), message).toBe(expected),
      }
    }
    return expect(value, message)
  }
  return { ...actual, expect: Object.assign(check, {
    poll: (read: () => Promise<unknown>) => ({
      toBe: async (expected: unknown) => expect(await read()).toBe(expected),
      toMatch: async (expected: RegExp) => expect(await read()).toMatch(expected),
      not: { toBe: async (expected: unknown) => expect(await read()).not.toBe(expected) },
    }),
  }) }
})

describe('openChildTabFromRow', () => {
  it('opens the exact absent child and preserves the added-tab check', async () => {
    const state: NavigationState = { ids: ['parent'], selectedId: 'parent', childId: 'actual-child' }
    const view = navigation(state, () => {
      state.ids.push('actual-child')
      state.selectedId = 'actual-child'
    })
    expect(await openChildTabFromRow(view.page, view.row)).toBe('actual-child')
    expect(view.click).toHaveBeenCalledTimes(1)
    expect(state.ids).toEqual(['parent', 'actual-child'])
    expect(state.selectedId).toBe('actual-child')
  })

  it('selects the existing native child without requiring another tab', async () => {
    const state: NavigationState = { ids: ['parent', 'actual-child'], selectedId: 'parent', childId: 'actual-child' }
    const view = navigation(state, () => state.selectedId = 'actual-child')
    expect(await openChildTabFromRow(view.page, view.row)).toBe('actual-child')
    expect(state.ids).toEqual(['parent', 'actual-child'])
    expect(state.selectedId).toBe('actual-child')
  })

  it('keeps an already selected child on a repeated row click', async () => {
    const state: NavigationState = { ids: ['parent', 'actual-child'], selectedId: 'actual-child', childId: 'actual-child' }
    const view = navigation(state, () => {})
    expect(await openChildTabFromRow(view.page, view.row)).toBe('actual-child')
    expect(view.click).toHaveBeenCalledTimes(1)
    expect(state.ids).toEqual(['parent', 'actual-child'])
  })

  it.each([null, '', '   '])('refuses an absent native child ID before clicking: %j', async (childId) => {
    const state: NavigationState = { ids: ['parent'], selectedId: 'parent', childId }
    const view = navigation(state, () => {
      state.ids.push('unrelated-child')
      state.selectedId = 'unrelated-child'
    })
    await expect(openChildTabFromRow(view.page, view.row)).rejects.toThrow()
    expect(view.click).not.toHaveBeenCalled()
  })

  it('refuses an unrelated new tab even when the count increases correctly', async () => {
    const state: NavigationState = { ids: ['parent'], selectedId: 'parent', childId: 'actual-child' }
    const view = navigation(state, () => {
      state.ids.push('other-child')
      state.selectedId = 'other-child'
    })
    await expect(openChildTabFromRow(view.page, view.row)).rejects.toThrow()
  })

  it('requires the exact opened child to become selected', async () => {
    const state: NavigationState = { ids: ['parent'], selectedId: 'parent', childId: 'actual-child' }
    const view = navigation(state, () => state.ids.push('actual-child'))
    await expect(openChildTabFromRow(view.page, view.row)).rejects.toThrow()
  })

  it('refuses duplicate rendered tab IDs before clicking', async () => {
    const state: NavigationState = { ids: ['parent', 'parent'], selectedId: 'parent', childId: 'actual-child' }
    const view = navigation(state, () => state.ids.push('actual-child'))
    await expect(openChildTabFromRow(view.page, view.row)).rejects.toThrow()
    expect(view.click).not.toHaveBeenCalled()
  })
})
