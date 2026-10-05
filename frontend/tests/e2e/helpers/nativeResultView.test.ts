import type { Locator, Page } from '@playwright/test'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { expandNativeResultView, nativeResultBubble } from './nativeResultView'

function strictHandle<T extends object>(methods: Partial<T>): T {
  return new Proxy(methods as T, {
    get: (target, property, receiver) => {
      if (property in target)
        return Reflect.get(target, property, receiver)
      if (typeof property === 'symbol')
        return undefined
      throw new Error(`The result-view unit accessed an absent method: ${String(property)}.`)
    },
  })
}

function fixture(expanded: boolean) {
  const state = { expanded }
  const events: string[] = []
  const view = strictHandle<Locator>({
    hover: async () => {
      events.push('hover')
    },
    getByRole: (_role, options) => strictHandle<Locator>({
      click: async () => {
        expect(options).toEqual({ name: 'Expand', exact: true })
        if (state.expanded)
          throw new Error('The controlled result has no Expand action while expanded.')
        events.push('expand')
        state.expanded = true
      },
      isVisible: async () => options?.name === 'Collapse' && state.expanded,
    }),
  })
  const result = strictHandle<Locator>({ locator: (selector) => {
    expect(selector).toBe('..')
    return view
  } })
  return { result, state, events }
}

vi.mock('@playwright/test', async (importOriginal) => {
  const original = await importOriginal<typeof import('@playwright/test')>()
  return {
    ...original,
    expect: (locator: Pick<Locator, 'isVisible'>) => ({
      toBeVisible: async () => expect(await locator.isVisible()).toBe(true),
    }),
  }
})

beforeEach(() => vi.clearAllMocks())

describe('expandNativeResultView', () => {
  it('uses the result parent toolbar and expands before it requires the full view', async () => {
    const current = fixture(false)
    await expandNativeResultView(current.result)
    expect(current.state.expanded).toBe(true)
    expect(current.events).toEqual(['hover', 'expand'])
  })

  it('expands retained output again after a new page restores its collapsed default', async () => {
    const current = fixture(false)
    await expandNativeResultView(current.result)
    current.state.expanded = false
    await expandNativeResultView(current.result)
    expect(current.events).toEqual(['hover', 'expand', 'hover', 'expand'])
  })

  it('rejects a view without its actual Expand action', async () => {
    await expect(expandNativeResultView(fixture(true).result)).rejects.toThrow('no Expand action')
  })
})

describe('nativeResultBubble', () => {
  /** A page whose `locator` returns the selector, so a test reads the exact selector that the helper builds. */
  function selectorPage(): Page {
    return Object.assign({} as Page, { locator: (selector: string) => selector })
  }

  it('selects the visible result row of the exact call ID', () => {
    expect(nativeResultBubble(selectorPage(), 'live-child-read')).toBe(
      '[data-testid="message-bubble"][data-tool-call-id="live-child-read"][data-tool-row-role="result"]:visible',
    )
  })

  it('escapes a quote and a backslash in the call ID for a quoted attribute value', () => {
    expect(nativeResultBubble(selectorPage(), 'a"b\\c')).toBe(
      '[data-testid="message-bubble"][data-tool-call-id="a\\"b\\\\c"][data-tool-row-role="result"]:visible',
    )
  })

  it('keeps an empty call ID as an empty attribute value, so it matches no real call', () => {
    expect(nativeResultBubble(selectorPage(), '')).toContain('[data-tool-call-id=""]')
  })
})
