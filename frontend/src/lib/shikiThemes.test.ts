import type { SyntaxThemePair } from '~/lib/syntaxThemes'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createPairKeyedCache, dualThemeTokenOptions, setSyntaxThemePair, syntaxThemeKey, syntaxThemePair } from '~/lib/shikiThemes'
import { syntaxPairFor } from '~/lib/syntaxThemes'

const DEFAULT_PAIR = syntaxPairFor('default')

afterEach(() => {
  // Module-level state, shared by every case in this file.
  setSyntaxThemePair(DEFAULT_PAIR)
})

// This module holds the one piece of mutable state the whole highlighting stack
// reads. Two things depend on it being exactly right: the options every Shiki
// call site spreads, and the key every cache that holds tokenized output folds
// in. A stale read on either serves one theme's colours under another's name.
describe('syntaxThemePair', () => {
  it('starts on the default theme pair', () => {
    expect(syntaxThemePair()).toEqual(DEFAULT_PAIR)
  })

  it('reports whether the pair actually moved', () => {
    // The return value is what lets the store skip an invalidation and a
    // re-render for a change that changes nothing.
    expect(setSyntaxThemePair({ light: 'ayu-light', dark: 'ayu-dark' })).toBe(true)
    expect(setSyntaxThemePair({ light: 'ayu-light', dark: 'ayu-dark' })).toBe(false)
    expect(setSyntaxThemePair({ light: 'ayu-light', dark: 'nord' })).toBe(true)
  })

  it('treats a half-change as a change', () => {
    setSyntaxThemePair({ light: 'one-light', dark: 'one-dark-pro' })
    expect(setSyntaxThemePair({ light: 'one-light', dark: 'nord' })).toBe(true)
    expect(setSyntaxThemePair({ light: 'github-light', dark: 'nord' })).toBe(true)
  })
})

describe('syntaxThemeKey', () => {
  it('distinguishes every pair, including one that shares a half', () => {
    // Folded into both IndexedDB namespaces, so two pairs that collide here
    // would serve each other's persisted tokens after a reload.
    const keys = new Set<string>()
    for (const pair of [
      { light: 'github-light', dark: 'github-dark' },
      { light: 'github-light', dark: 'nord' },
      { light: 'nord-light', dark: 'github-dark' },
      { light: 'nord-light', dark: 'nord' },
    ]) {
      setSyntaxThemePair(pair)
      keys.add(syntaxThemeKey())
    }
    expect(keys.size).toBe(4)
  })

  it('follows the current pair rather than the pair at import time', () => {
    const before = syntaxThemeKey()
    setSyntaxThemePair({ light: 'solarized-light', dark: 'solarized-dark' })
    expect(syntaxThemeKey()).not.toBe(before)
    expect(syntaxThemeKey()).toContain('solarized-light')
    expect(syntaxThemeKey()).toContain('solarized-dark')
  })
})

describe('dualThemeTokenOptions', () => {
  it('names the CURRENT pair, not the one captured at module evaluation', () => {
    // A call site that captured this as a constant would keep tokenizing with
    // whatever was current when its module first evaluated -- which is exactly
    // the bug that made it a function.
    setSyntaxThemePair({ light: 'catppuccin-latte', dark: 'catppuccin-mocha' })
    expect(dualThemeTokenOptions().themes).toEqual({
      light: 'catppuccin-latte',
      dark: 'catppuccin-mocha',
    })
  })

  it('keeps defaultColor off under every pair', () => {
    // `defaultColor: false` is what makes Shiki emit per-token --shiki-light /
    // --shiki-dark variables instead of one baked colour. Every CSS rule that
    // themes highlighted code keys off exactly those, so a pair that turned it
    // on would render code in one fixed variant.
    for (const pair of [DEFAULT_PAIR, { light: 'nord-light', dark: 'nord' }]) {
      setSyntaxThemePair(pair)
      expect(dualThemeTokenOptions().defaultColor).toBe(false)
    }
  })

  it('keys the emitted variables on light/dark, never on the theme names', () => {
    // The CSS contract does not change with the theme precisely because Shiki
    // names its variables after these KEYS. If this object ever grew a third
    // key, or renamed one, every `var(--shiki-light)` rule would go dead.
    setSyntaxThemePair({ light: 'gruvbox-light-medium', dark: 'gruvbox-dark-medium' })
    expect(Object.keys(dualThemeTokenOptions().themes).sort()).toEqual(['dark', 'light'])
  })
})

// The shape both markdown processors used to hand-roll. Each baked the pair in
// at construction, so an unkeyed cache went on tokenizing in the FIRST pair's
// colours for the life of the isolate however many themes the user tried.
describe('createPairKeyedCache', () => {
  const pairA: SyntaxThemePair = { light: 'github-light', dark: 'github-dark' }
  const pairB: SyntaxThemePair = { light: 'nord', dark: 'nord' }

  it('builds once and reuses for the same pair', () => {
    const build = vi.fn(() => ({}))
    const cache = createPairKeyedCache<object>()

    const first = cache(pairA, build)
    expect(cache(pairA, build)).toBe(first)
    expect(build).toHaveBeenCalledTimes(1)
  })

  // The pair is compared by VALUE, so a caller that rebuilds an equal object
  // every call -- which is what a worker message does -- must still hit.
  it('reuses for an equal pair that is a different object', () => {
    const build = vi.fn(() => ({}))
    const cache = createPairKeyedCache<object>()

    const first = cache(pairA, build)
    expect(cache({ ...pairA }, build)).toBe(first)
    expect(build).toHaveBeenCalledTimes(1)
  })

  it('rebuilds when either half of the pair moves', () => {
    const build = vi.fn(() => ({}))
    const cache = createPairKeyedCache<object>()

    const first = cache(pairA, build)
    const second = cache(pairB, build)
    expect(second).not.toBe(first)
    expect(build).toHaveBeenCalledTimes(2)

    // One half is enough: a theme with the same light side and a new dark one
    // paints different colours in the dark.
    cache({ light: pairB.light, dark: 'ayu-dark' }, build)
    expect(build).toHaveBeenCalledTimes(3)
  })

  // Going back is a rebuild, not a hit: ONE slot, so a map of every pair the
  // user ever sampled cannot accumulate.
  it('holds one entry, so returning to an earlier pair rebuilds', () => {
    const build = vi.fn(() => ({}))
    const cache = createPairKeyedCache<object>()

    const first = cache(pairA, build)
    cache(pairB, build)
    expect(cache(pairA, build)).not.toBe(first)
    expect(build).toHaveBeenCalledTimes(3)
  })

  it('keeps two caches independent', () => {
    const cacheA = createPairKeyedCache<string>()
    const cacheB = createPairKeyedCache<string>()

    expect(cacheA(pairA, () => 'a')).toBe('a')
    expect(cacheB(pairA, () => 'b')).toBe('b')
    expect(cacheA(pairA, () => 'never')).toBe('a')
  })
})
