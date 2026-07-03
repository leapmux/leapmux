import { beforeEach, describe, expect, it } from 'vitest'
import {
  _resetShikiStyleClassesForTest,
  collectShikiStyles,
  ensureShikiStyleRules,
  recordShikiStyle,
  shikiStyleClassName,
  shikiStyleClassTransformer,
  shikiStyleDecl,
} from './shikiStyleClass'

beforeEach(() => {
  _resetShikiStyleClassesForTest()
})

function styleElement(): HTMLStyleElement | null {
  return document.querySelector('style[data-shiki-style-classes]')
}

/**
 * The injected rules read from the CSSOM sheet (insertRule lives there, NOT in
 * textContent), each as { selector, light, dark }. jsdom serializes cssText with its own
 * spacing, so assert on selectorText + the resolved custom-property values, not raw text.
 */
function injectedRules(): Array<{ selector: string, light: string, dark: string }> {
  const el = styleElement()
  if (!el?.sheet)
    return []
  return Array.from(el.sheet.cssRules).map((rule) => {
    const styleRule = rule as CSSStyleRule
    return {
      selector: styleRule.selectorText,
      light: styleRule.style.getPropertyValue('--shiki-light').trim(),
      dark: styleRule.style.getPropertyValue('--shiki-dark').trim(),
    }
  })
}

describe('shikistyledecl', () => {
  it('passes a string style through unchanged', () => {
    expect(shikiStyleDecl('--shiki-light:#24292E;--shiki-dark:#E1E4E8')).toBe('--shiki-light:#24292E;--shiki-dark:#E1E4E8')
  })

  it('serializes an object style exactly like Shiki\'s tokensToHast (key:value;key:value)', () => {
    // Same form the HTML transformer reads from the style attribute, so the
    // token path and the HTML path mint identical classes for identical styles.
    expect(shikiStyleDecl({ '--shiki-light': '#24292E', '--shiki-dark': '#E1E4E8' }))
      .toBe('--shiki-light:#24292E;--shiki-dark:#E1E4E8')
  })

  it('returns empty for an empty style object (unstyled token)', () => {
    expect(shikiStyleDecl({})).toBe('')
  })
})

describe('shikistyleclassname', () => {
  it('is deterministic and prefixed', () => {
    const name = shikiStyleClassName('--shiki-light:#abc')
    expect(name).toBe(shikiStyleClassName('--shiki-light:#abc'))
    expect(name).toMatch(/^sk-[0-9a-f]{8}-[0-9a-z]+$/)
  })

  it('folds the declaration length in as a digest-collision guard', () => {
    // Two declarations of different lengths can never share a class, even on a
    // 32-bit digest collision (the artifactKey pattern).
    expect(shikiStyleClassName('a')).toMatch(/-1$/)
    expect(shikiStyleClassName('ab')).toMatch(/-2$/)
  })
})

describe('recordshikistyle', () => {
  it('mints a class, injects its rule once, and is idempotent', () => {
    const name = recordShikiStyle('--shiki-light:#abc;--shiki-dark:#def')
    expect(name).toBe(shikiStyleClassName('--shiki-light:#abc;--shiki-dark:#def'))
    expect(recordShikiStyle('--shiki-light:#abc;--shiki-dark:#def')).toBe(name)
    const rules = injectedRules()
    // Injected exactly once despite two record calls.
    expect(rules).toHaveLength(1)
    expect(rules[0].selector).toBe(`.${name}`)
    expect(rules[0].light).toBe('#abc')
    expect(rules[0].dark).toBe('#def')
  })

  it('injects via the CSSOM sheet (insertRule), not a textContent re-parse', () => {
    // The fix replaces per-rule `textContent +=` (which re-parses the WHOLE sheet -> O(N^2)
    // over distinct declarations) with insertRule. The behavioral signature: rules live in
    // the CSSOM sheet, so the <style> element's textContent stays empty even though the rule
    // is present and queryable.
    const name = recordShikiStyle('--shiki-light:#abc')!
    const el = styleElement()!
    expect(el.textContent).toBe('')
    expect(el.sheet!.cssRules).toHaveLength(1)
    expect((el.sheet!.cssRules[0] as CSSStyleRule).selectorText).toBe(`.${name}`)
  })

  it('returns undefined for the empty declaration and injects nothing', () => {
    expect(recordShikiStyle('')).toBeUndefined()
    expect(styleElement()).toBeNull()
  })
})

describe('ensureshikistylerules', () => {
  it('injects rules for a worker-shipped dictionary, skipping already-known classes', () => {
    const known = recordShikiStyle('--shiki-light:#111')!
    const foreign = shikiStyleClassName('--shiki-light:#222')
    ensureShikiStyleRules({
      [known]: '--shiki-light:#111',
      [foreign]: '--shiki-light:#222',
    })
    const rules = injectedRules()
    const bySelector = new Map(rules.map(r => [r.selector, r.light]))
    expect(bySelector.get(`.${known}`)).toBe('#111')
    expect(bySelector.get(`.${foreign}`)).toBe('#222')
    // The known class was injected exactly once (recorded first, then skipped in the dict).
    expect(rules).toHaveLength(2)
    // Re-shipping the same (full) dictionary adds nothing -- the worker sends
    // its accumulated superset with every response.
    ensureShikiStyleRules({ [foreign]: '--shiki-light:#222' })
    expect(injectedRules()).toHaveLength(2)
  })

  it('injects every new rule in a dictionary exactly once', () => {
    const a = shikiStyleClassName('--shiki-light:#a1')
    const b = shikiStyleClassName('--shiki-light:#b2')
    const c = shikiStyleClassName('--shiki-light:#c3')
    ensureShikiStyleRules({
      [a]: '--shiki-light:#a1',
      [b]: '--shiki-light:#b2',
      [c]: '--shiki-light:#c3',
    })
    const rules = injectedRules()
    expect(rules).toHaveLength(3)
    const bySelector = new Map(rules.map(r => [r.selector, r.light]))
    expect(bySelector.get(`.${a}`)).toBe('#a1')
    expect(bySelector.get(`.${b}`)).toBe('#b2')
    expect(bySelector.get(`.${c}`)).toBe('#c3')
  })
})

describe('collectshikistyles', () => {
  it('snapshots every recorded declaration keyed by class name', () => {
    const a = recordShikiStyle('--shiki-light:#a')!
    const b = recordShikiStyle('--shiki-light:#b')!
    expect(collectShikiStyles()).toEqual({
      [a]: '--shiki-light:#a',
      [b]: '--shiki-light:#b',
    })
  })
})

describe('shikistyleclasstransformer', () => {
  const transformer = () => shikiStyleClassTransformer().span! as (node: any) => any

  it('moves a token span\'s inline style into a shared class', () => {
    const node = {
      type: 'element',
      tagName: 'span',
      properties: { style: '--shiki-light:#abc' },
      children: [],
    }
    transformer().call({}, node)
    expect(node.properties.style).toBeUndefined()
    expect(node.properties).toHaveProperty('class', shikiStyleClassName('--shiki-light:#abc'))
    expect(collectShikiStyles()).toEqual({ [shikiStyleClassName('--shiki-light:#abc')]: '--shiki-light:#abc' })
  })

  it('appends to an existing class and leaves unstyled spans untouched', () => {
    const styled = { type: 'element', tagName: 'span', properties: { class: 'line', style: '--shiki-light:#abc' }, children: [] }
    transformer().call({}, styled)
    expect(styled.properties.class).toBe(`line ${shikiStyleClassName('--shiki-light:#abc')}`)

    const plain = { type: 'element', tagName: 'span', properties: {}, children: [] }
    transformer().call({}, plain)
    expect(plain.properties).toEqual({})
  })

  it('appends into an existing ARRAY class without stringifying it (hast allows string[])', () => {
    const node = { type: 'element', tagName: 'span', properties: { class: ['a', 'b'], style: '--shiki-light:#abc' }, children: [] }
    transformer().call({}, node)
    // Array form is preserved and the shared class is appended -- NOT `String(['a','b'])`
    // which would produce the invalid comma-joined token "a,b sk-...".
    expect(node.properties.class).toEqual(['a', 'b', shikiStyleClassName('--shiki-light:#abc')])
  })
})
