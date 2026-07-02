import { beforeEach, describe, expect, it } from 'vitest'
import { _resetShikiStyleClassesForTest, shikiStyleClassName } from './shikiStyleClass'
import { readInjectedShikiRules } from './shikiStyleClass.testkit'
import { _resetTokenCache, _TOKEN_CACHE_MAX_SIZE, expandInternedTokenLines, getCachedTokens, internTokenLines, makeKey, mergeLineTokens, setCachedTokens, toCachedTokens } from './tokenCache'

beforeEach(() => {
  _resetShikiStyleClassesForTest()
})

describe('makekey', () => {
  // The NUL separator, built via fromCharCode so this source file stays plain ASCII.
  const NUL = String.fromCharCode(0)

  it('joins lang and code with a NUL separator', () => {
    expect(makeKey('typescript', 'const x = 1')).toBe(`typescript${NUL}const x = 1`)
  })

  it('is collision-free across the lang/code boundary (why the NUL separator matters)', () => {
    // A naive `lang + code` join would collide: ('ab','c') and ('a','bc') both -> 'abc'.
    // NUL can't appear in a Shiki language id, so the split point is unambiguous.
    expect(makeKey('ab', 'c')).not.toBe(makeKey('a', 'bc'))
  })

  it('is the exact key the cache stores under (single-sourced across consumers)', () => {
    _resetTokenCache()
    const tokens = toCachedTokens([[{ content: 'x' }]])
    setCachedTokens('json', '{"a":1}', tokens)
    // A caller that rebuilds the key via makeKey addresses the same entry the cache wrote.
    expect(makeKey('json', '{"a":1}')).toBe(`json${NUL}{"a":1}`)
    expect(getCachedTokens('json', '{"a":1}')).toEqual(tokens)
  })
})

describe('tocachedtokens', () => {
  it('projects each token to content + a shared style class, dropping other fields', () => {
    // Shiki ThemedTokens carry extra fields (offset, color, fontStyle, ...) the
    // renderer never reads. The projection keeps ONLY content + the minted
    // className (see shikiStyleClass) -- the style itself lives in a CSS rule.
    const lines = [
      [
        { content: 'const', htmlStyle: { '--shiki-light': '#abc' }, offset: 0, color: '#abc', fontStyle: 1 },
        { content: ' x', htmlStyle: '--shiki-light:#def', offset: 5 },
      ],
      [],
    ]
    const result = toCachedTokens(lines as any)
    expect(result).toEqual([
      [
        { content: 'const', className: shikiStyleClassName('--shiki-light:#abc') },
        { content: ' x', className: shikiStyleClassName('--shiki-light:#def') },
      ],
      [],
    ])
    // The extra fields are gone, not merely undefined.
    expect(Object.keys(result[0][0])).toEqual(['content', 'className'])
  })

  it('mints identical class names for canonically equal string and object styles', () => {
    const result = toCachedTokens([[
      { content: 'a', htmlStyle: { '--shiki-light': '#abc' } },
      { content: 'b', htmlStyle: '--shiki-light:#abc' },
    ]])
    expect(result[0][0].className).toBe(result[0][1].className)
  })

  it('injects one CSS rule per distinct style into the shared style element', () => {
    toCachedTokens([[
      { content: 'a', htmlStyle: { '--shiki-light': '#a1', '--shiki-dark': '#b2' } },
      { content: 'b', htmlStyle: { '--shiki-light': '#a1', '--shiki-dark': '#b2' } },
      { content: 'c', htmlStyle: { '--shiki-light': '#c3' } },
    ]])
    const rules = readInjectedShikiRules()
    expect(rules).toContain(`.${shikiStyleClassName('--shiki-light:#a1;--shiki-dark:#b2')}{--shiki-light:#a1;--shiki-dark:#b2}`)
    expect(rules).toContain(`.${shikiStyleClassName('--shiki-light:#c3')}{--shiki-light:#c3}`)
    // The duplicate style minted no second rule.
    expect(rules.match(/\.sk-/g)).toHaveLength(2)
  })

  it('leaves unstyled and empty-style tokens class-free', () => {
    const result = toCachedTokens([[{ content: 'x' }, { content: 'y', htmlStyle: {} }]])
    expect(result).toEqual([[{ content: 'x' }, { content: 'y' }]])
    expect(result[0][0].className).toBeUndefined()
    expect(result[0][1].className).toBeUndefined()
  })

  it('round-trips through the LRU cache', () => {
    const tokens = toCachedTokens([[{ content: 'a', htmlStyle: '--shiki-light:#1' }]])
    setCachedTokens('json', '{"a":1}', tokens)
    expect(getCachedTokens('json', '{"a":1}')).toEqual(tokens)
    expect(getCachedTokens('json', 'other')).toBeUndefined()
  })
})

describe('mergelinetokens', () => {
  const light = (color: string) => ({ '--shiki-light': color })

  it('folds a leading whitespace-only token into the next token', () => {
    const merged = mergeLineTokens([[
      { content: '    ', htmlStyle: light('#default') },
      { content: 'when', htmlStyle: light('#keyword') },
    ]])
    expect(merged).toEqual([[{ content: '    when', htmlStyle: light('#keyword') }]])
  })

  it('folds consecutive whitespace-only tokens as one carry', () => {
    const merged = mergeLineTokens([[
      { content: '  ', htmlStyle: light('#a') },
      { content: '\t', htmlStyle: light('#b') },
      { content: 'x', htmlStyle: light('#c') },
    ]])
    expect(merged).toEqual([[{ content: '  \tx', htmlStyle: light('#c') }]])
  })

  it('preserves each line\'s total content (the word-diff walker\'s invariant)', () => {
    const lines = [
      [{ content: '  ' }, { content: 'a', htmlStyle: light('#1') }, { content: ' ' }, { content: 'b', htmlStyle: light('#1') }],
      [{ content: 'lone' }],
      [{ content: '   ' }],
      [],
    ]
    const merged = mergeLineTokens(lines)
    merged.forEach((line, i) => {
      const original = lines[i].map(t => t.content).join('')
      expect(line.map(t => t.content).join('')).toBe(original)
    })
  })

  it('keeps a trailing whitespace-only token (nothing to merge into)', () => {
    const merged = mergeLineTokens([[
      { content: 'x', htmlStyle: light('#a') },
      { content: '   ', htmlStyle: light('#b') },
    ]])
    // Total content preserved; the trailing whitespace survives in some token.
    expect(merged[0].map(t => t.content).join('')).toBe('x   ')
  })

  it('does NOT fold whitespace that paints its own background (ANSI color bar)', () => {
    const bar = { '--shiki-light': '#fff', '--shiki-light-bg': '#f00' }
    const merged = mergeLineTokens([[
      { content: '   ', htmlStyle: bar },
      { content: 'x', htmlStyle: light('#a') },
    ]])
    expect(merged).toEqual([[
      { content: '   ', htmlStyle: bar },
      { content: 'x', htmlStyle: light('#a') },
    ]])
  })

  it('does NOT extend a background or text-decoration leftward over folded whitespace', () => {
    const decorated = { '--shiki-light': '#a', '--shiki-light-text-decoration': 'underline' }
    const merged = mergeLineTokens([[
      { content: '  ', htmlStyle: light('#d') },
      { content: 'u', htmlStyle: decorated },
    ]])
    // The whitespace is kept as its own (unstyled) token; the decorated token
    // is untouched -- an underline must not run under the indentation.
    expect(merged).toEqual([[
      { content: '  ' },
      { content: 'u', htmlStyle: decorated },
    ]])

    // Same for a background-painting receiver: its color bar must not grow
    // leftward over the folded spaces (the other half of the receiver guard).
    const bar = { '--shiki-light': '#fff', '--shiki-light-bg': '#f00' }
    expect(mergeLineTokens([[
      { content: '  ', htmlStyle: light('#d') },
      { content: 'X', htmlStyle: bar },
    ]])).toEqual([[
      { content: '  ' },
      { content: 'X', htmlStyle: bar },
    ]])
  })

  it('keeps self-decorated whitespace as its own token (underlined spaces are visible ink)', () => {
    const underlinedWs = { '--shiki-light': '#a', '--shiki-light-text-decoration': 'underline' }
    const merged = mergeLineTokens([[
      { content: '  ', htmlStyle: underlinedWs },
      { content: 'x', htmlStyle: light('#b') },
    ]])
    // The whitespace's own decoration renders, so it must not be folded away
    // into the next token (which would drop the underline under the spaces).
    expect(merged).toEqual([[
      { content: '  ', htmlStyle: underlinedWs },
      { content: 'x', htmlStyle: light('#b') },
    ]])
  })

  it('concatenates adjacent tokens with identical declarations', () => {
    const merged = mergeLineTokens([[
      { content: 'a', htmlStyle: light('#1') },
      { content: 'b', htmlStyle: { '--shiki-light': '#1' } }, // equal contents, different object
      { content: 'c', htmlStyle: light('#2') },
    ]])
    expect(merged).toEqual([[
      { content: 'ab', htmlStyle: light('#1') },
      { content: 'c', htmlStyle: light('#2') },
    ]])
  })

  it('concatenates adjacent unstyled tokens', () => {
    const merged = mergeLineTokens([[{ content: 'a' }, { content: 'b' }]])
    expect(merged).toEqual([[{ content: 'ab' }]])
  })

  it('does NOT concatenate text-decorated runs (upstream mergeSameStyleTokens parity)', () => {
    const decorated = { '--shiki-light': '#a', '--shiki-light-text-decoration': 'underline' }
    const merged = mergeLineTokens([[
      { content: 'a', htmlStyle: { ...decorated } },
      { content: 'b', htmlStyle: { ...decorated } },
    ]])
    expect(merged[0]).toHaveLength(2)
  })

  it('handles empty lines and empty input', () => {
    expect(mergeLineTokens([])).toEqual([])
    expect(mergeLineTokens([[]])).toEqual([[]])
  })
})

describe('token cache lru eviction', () => {
  const tok = (s: string) => toCachedTokens([[{ content: s }]])

  beforeEach(() => {
    _resetTokenCache()
  })

  it('evicts the oldest entry once the capacity bound is exceeded', () => {
    for (let i = 0; i < _TOKEN_CACHE_MAX_SIZE; i++)
      setCachedTokens('json', `c-${i}`, tok(String(i)))
    // One past capacity evicts the oldest (c-0).
    setCachedTokens('json', 'c-overflow', tok('x'))
    expect(getCachedTokens('json', 'c-0')).toBeUndefined()
    expect(getCachedTokens('json', 'c-overflow')).toEqual(tok('x'))
  })

  it('overwriting an existing key at capacity evicts nothing', () => {
    // Regression for the bespoke "evict-before-set" path: overwriting a key that already
    // exists doesn't grow the map, so the bound isn't exceeded and no live entry should
    // be dropped. The old code evicted the oldest unconditionally on any set at capacity.
    for (let i = 0; i < _TOKEN_CACHE_MAX_SIZE; i++)
      setCachedTokens('json', `c-${i}`, tok(String(i)))
    setCachedTokens('json', 'c-10', tok('updated')) // overwrite, no read first (a read reorders)
    expect(getCachedTokens('json', 'c-0')).toBeDefined() // oldest survived
    expect(getCachedTokens('json', 'c-10')).toEqual(tok('updated'))
  })

  it('a cache read refreshes LRU position so the touched entry outlives eviction', () => {
    for (let i = 0; i < _TOKEN_CACHE_MAX_SIZE; i++)
      setCachedTokens('json', `c-${i}`, tok(String(i)))
    // Touch the oldest -> it becomes most-recently-used.
    expect(getCachedTokens('json', 'c-0')).toBeDefined()
    // A fresh insert now evicts the NEXT-oldest (c-1), not the refreshed c-0.
    setCachedTokens('json', 'c-fresh', tok('y'))
    expect(getCachedTokens('json', 'c-0')).toBeDefined()
    expect(getCachedTokens('json', 'c-1')).toBeUndefined()
  })
})

describe('interned token wire shape', () => {
  it('round-trips intern -> expand to the toCachedTokens projection', () => {
    const raw = [
      [
        { content: 'const', htmlStyle: { 'color': '#a', '--shiki-dark': '#b' } },
        { content: ' x', htmlStyle: { 'color': '#c', '--shiki-dark': '#d' } },
      ],
      [
        { content: 'plain' }, // no style
        { content: '= 1', htmlStyle: { 'color': '#a', '--shiki-dark': '#b' } },
      ],
    ]
    expect(expandInternedTokenLines(internTokenLines(raw))).toEqual(toCachedTokens(raw))
  })

  it('interns one styles entry per DISTINCT style and shares its class on expand', () => {
    const style = { 'color': '#f00', '--shiki-dark': '#0f0' }
    const raw = [[
      { content: 'a', htmlStyle: { ...style } },
      { content: 'b', htmlStyle: { ...style } }, // equal contents, different object
      { content: 'c', htmlStyle: { color: '#00f' } },
    ]]
    const interned = internTokenLines(raw)
    expect(interned.styles).toHaveLength(2)
    const expanded = expandInternedTokenLines(interned)
    // The two equal-style tokens share ONE class-name string after expansion.
    expect(expanded[0][0].className).toBe(expanded[0][1].className)
    expect(expanded[0][0].className).not.toBe(expanded[0][2].className)
  })

  it('keeps a string style distinct from an object style with the same JSON text in the wire shape', () => {
    const objStyle = { color: 'red' }
    const strStyle = JSON.stringify(objStyle) // the intern-key collision candidate
    const raw = [[
      { content: 'a', htmlStyle: objStyle },
      { content: 'b', htmlStyle: strStyle },
    ]]
    // Assert at the intern level only: a JSON-text string style is not real CSS,
    // so minting a class for it (expansion) would just inject an inert rule.
    expect(internTokenLines(raw).styles).toHaveLength(2)
  })

  it('mints distinct classes for canonically different declarations on expand', () => {
    const raw = [[
      { content: 'a', htmlStyle: { color: 'red' } }, // canonical decl 'color:red'
      { content: 'b', htmlStyle: 'color: red' }, // same computed style, different decl text
    ]]
    const interned = internTokenLines(raw)
    expect(interned.styles).toHaveLength(2)
    // Class names derive from the declaration TEXT, so these differ -- harmless
    // duplication (both rules are correct), never a wrong style.
    const expanded = expandInternedTokenLines(interned)
    expect(expanded[0][0].className).not.toBe(expanded[0][1].className)
  })

  it('handles empty lines and unstyled tokens (-1 index)', () => {
    expect(internTokenLines([])).toEqual({ styles: [], lines: [] })
    expect(expandInternedTokenLines({ styles: [], lines: [] })).toEqual([])
    const interned = internTokenLines([[{ content: 'x' }], []])
    expect(interned).toEqual({ styles: [], lines: [[[-1, 'x']], []] })
    expect(expandInternedTokenLines(interned)).toEqual([[{ content: 'x' }], []])
  })
})
