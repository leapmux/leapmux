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

describe('shikiStyleDecl', () => {
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

describe('shikiStyleClassName', () => {
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

describe('recordShikiStyle', () => {
  it('mints a class, injects its rule once, and is idempotent', () => {
    const name = recordShikiStyle('--shiki-light:#abc;--shiki-dark:#def')
    expect(name).toBe(shikiStyleClassName('--shiki-light:#abc;--shiki-dark:#def'))
    expect(recordShikiStyle('--shiki-light:#abc;--shiki-dark:#def')).toBe(name)
    const el = styleElement()!
    expect(el.textContent).toBe(`.${name}{--shiki-light:#abc;--shiki-dark:#def}`)
  })

  it('returns undefined for the empty declaration and injects nothing', () => {
    expect(recordShikiStyle('')).toBeUndefined()
    expect(styleElement()).toBeNull()
  })
})

describe('ensureShikiStyleRules', () => {
  it('injects rules for a worker-shipped dictionary, skipping already-known classes', () => {
    const known = recordShikiStyle('--shiki-light:#111')!
    const foreign = shikiStyleClassName('--shiki-light:#222')
    ensureShikiStyleRules({
      [known]: '--shiki-light:#111',
      [foreign]: '--shiki-light:#222',
    })
    const text = styleElement()!.textContent!
    expect(text).toContain(`.${known}{--shiki-light:#111}`)
    expect(text).toContain(`.${foreign}{--shiki-light:#222}`)
    // The known class was injected exactly once.
    expect(text.match(/#111/g)).toHaveLength(1)
    // Re-shipping the same (full) dictionary adds nothing -- the worker sends
    // its accumulated superset with every response.
    ensureShikiStyleRules({ [foreign]: '--shiki-light:#222' })
    expect(styleElement()!.textContent).toBe(text)
  })
})

describe('collectShikiStyles', () => {
  it('snapshots every recorded declaration keyed by class name', () => {
    const a = recordShikiStyle('--shiki-light:#a')!
    const b = recordShikiStyle('--shiki-light:#b')!
    expect(collectShikiStyles()).toEqual({
      [a]: '--shiki-light:#a',
      [b]: '--shiki-light:#b',
    })
  })
})

describe('shikiStyleClassTransformer', () => {
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
})
