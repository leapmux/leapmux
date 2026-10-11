import { describe, expect, it } from 'vitest'
import { cssAttributeValue } from './cssAttribute'
import { tabIdSelector } from './tabSelectors'

describe('tabIdSelector', () => {
  it('selects the tab ID attribute with a plain ID as it is', () => {
    expect(tabIdSelector('0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b')).toBe('[data-tab-id="0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"]')
  })

  it.each([
    ['a quote', 'tab"1'],
    ['a backslash', 'tab\\1'],
    ['a line break', 'tab\n1'],
  ])('escapes %s through cssAttributeValue, so the value stays one quoted CSS string', (_label, tabId) => {
    const selector = tabIdSelector(tabId)
    expect(selector).toBe(`[data-tab-id="${cssAttributeValue(tabId)}"]`)
    expect(selector).not.toContain(`"${tabId}"`)
  })

  it('keeps an empty ID as an empty attribute value, so it matches no real tab', () => {
    expect(tabIdSelector('')).toBe('[data-tab-id=""]')
  })
})
