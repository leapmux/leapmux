import type { ThemeRegistrationRaw } from 'shiki/core'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { setSyntaxThemePair, syntaxThemePair } from '~/lib/shikiThemes'
import { syntaxPairFor } from '~/lib/syntaxThemes'
import { onSyntaxThemeChange, resolveSyntaxPair, resolveSyntaxVariant, setSyntaxTheme, syntaxThemeGeneration } from '~/lib/syntaxThemeStore'

const MATCH_BOTH = { name: 'match-ui', mode: 'match-ui' } as const
const DEFAULT_PAIR = syntaxPairFor('default')

/** A registrar double that records the order its calls arrive in. */
function fakeRegistrar(log?: string[]) {
  const themes: string[] = []
  return {
    themes,
    getLoadedThemes: () => [...themes],
    loadTheme: async (theme: ThemeRegistrationRaw) => {
      const name = (theme as { name?: string }).name ?? 'unnamed'
      themes.push(name)
      log?.push(`register:${name}`)
    },
  }
}

afterEach(() => {
  setSyntaxThemePair(DEFAULT_PAIR)
})

describe('resolveSyntaxPair', () => {
  it('follows the UI palette under the sentinel', () => {
    expect(resolveSyntaxPair(MATCH_BOTH, { name: 'catppuccin', mode: 'dark' }, 'dark'))
      .toEqual(syntaxPairFor('catppuccin'))
  })

  it('emits BOTH halves while following, so the app repaints code in CSS', () => {
    // The pair the app is not showing costs nothing to carry -- Shiki puts both
    // in every token -- and carrying it is what makes the app's own light/dark
    // switch free of a re-tokenize.
    const pair = resolveSyntaxPair(MATCH_BOTH, { name: 'nord', mode: 'light' }, 'dark')
    expect(pair.light).not.toBe(pair.dark)
    expect(pair).toEqual(syntaxPairFor('nord'))
  })

  it('keeps its own palette when one is pinned', () => {
    const pinned = resolveSyntaxPair({ name: 'nord', mode: 'light' }, { name: 'catppuccin', mode: 'light' }, 'light')
    expect(pinned).toEqual({ light: syntaxPairFor('nord').light, dark: syntaxPairFor('nord').light })
  })

  it('falls back to the default pair for a palette this build does not carry', () => {
    expect(resolveSyntaxPair({ name: 'from-the-future', mode: 'dark' }, { name: 'nord', mode: 'dark' }, 'dark'))
      .toEqual({ light: syntaxPairFor('default').dark, dark: syntaxPairFor('default').dark })
  })

  it('collapses BOTH halves when the syntax mode is pinned', () => {
    // Shiki carries both halves of a pair in every token and CSS picks between
    // them by data-theme. So pinning the syntax mode cannot mean "emit only the
    // dark half" -- the light variable would still be the one the page reads
    // while the app is light. It means "make both halves that theme".
    const pinnedDark = resolveSyntaxPair({ name: 'gruvbox', mode: 'dark' }, { name: 'gruvbox', mode: 'light' }, 'light')
    expect(pinnedDark).toEqual({ light: syntaxPairFor('gruvbox').dark, dark: syntaxPairFor('gruvbox').dark })

    const pinnedLight = resolveSyntaxPair({ name: 'gruvbox', mode: 'light' }, { name: 'gruvbox', mode: 'dark' }, 'dark')
    expect(pinnedLight).toEqual({ light: syntaxPairFor('gruvbox').light, dark: syntaxPairFor('gruvbox').light })
  })

  it('reads the OS through `system`, independently of the app', () => {
    // `system` means the OS in every one of the three appearance rows. Reading
    // the app's resolved mode here would make one word mean two things.
    const underDarkOs = resolveSyntaxPair({ name: 'ayu', mode: 'system' }, { name: 'ayu', mode: 'light' }, 'dark')
    expect(underDarkOs).toEqual({ light: syntaxPairFor('ayu').dark, dark: syntaxPairFor('ayu').dark })

    const underLightOs = resolveSyntaxPair({ name: 'ayu', mode: 'system' }, { name: 'ayu', mode: 'dark' }, 'light')
    expect(underLightOs).toEqual({ light: syntaxPairFor('ayu').light, dark: syntaxPairFor('ayu').light })
  })

  it('ignores a stray pinned mode beside the sentinel palette', () => {
    // The other half of the same repair. `match-ui` in the NAME means the whole
    // choice follows the app, so a mode left beside it must not collapse the
    // pair -- that would pin code to one variant while the app switches freely.
    expect(resolveSyntaxPair({ name: 'match-ui', mode: 'dark' }, { name: 'ayu', mode: 'light' }, 'light'))
      .toEqual(syntaxPairFor('ayu'))
  })

  it('reads a stray sentinel mode as following the app, and stays total', () => {
    // A parsed preference cannot carry one sentinel alone -- the two halves
    // move together -- but this function answers for whatever it is handed
    // rather than throwing or emitting an unregistered theme name.
    expect(resolveSyntaxPair({ name: 'one', mode: 'match-ui' }, { name: 'nord', mode: 'dark' }, 'dark'))
      .toEqual(syntaxPairFor('one'))
  })
})

// The apply ORDER is the whole correctness argument of this store, and nothing
// else in the suite pins it. A synchronous call site cannot await a theme load,
// so pointing the shared options at a pair before its themes are registered
// makes every synchronous highlight throw `Theme ... not found`.
describe('resolveSyntaxVariant', () => {
  // The palette a code surface wears. It has to answer the SAME question the
  // pair answers -- which look is showing -- or the tokens and the surface they
  // land on disagree, which is how a dark syntax theme came to paint bright
  // tokens onto a light page.
  it('wears the UI variant under the sentinel', () => {
    expect(resolveSyntaxVariant(MATCH_BOTH, { name: 'catppuccin', mode: 'dark' }, 'light', 'dark').id)
      .toBe('catppuccin-mocha')
    expect(resolveSyntaxVariant(MATCH_BOTH, { name: 'catppuccin', mode: 'light' }, 'dark', 'light').id)
      .toBe('catppuccin-latte')
  })

  it('carries the UI flavour, not just the UI palette', () => {
    // A user on Macchiato must not get code on Mocha's background.
    expect(resolveSyntaxVariant(
      MATCH_BOTH,
      { name: 'catppuccin', mode: 'dark', variant: { dark: 'catppuccin-macchiato' } },
      'dark',
      'dark',
    ).id).toBe('catppuccin-macchiato')
  })

  it('wears the pinned palette at the pinned polarity, against the app', () => {
    // THE COMBINATION THIS EXISTS FOR: dark code inside a light app.
    expect(resolveSyntaxVariant({ name: 'gruvbox', mode: 'dark' }, { name: 'default', mode: 'light' }, 'light', 'light').id)
      .toBe('gruvbox-dark-medium')
    expect(resolveSyntaxVariant({ name: 'gruvbox', mode: 'light' }, { name: 'default', mode: 'dark' }, 'dark', 'dark').id)
      .toBe('gruvbox-light-medium')
  })

  it('honours a pinned contrast level', () => {
    expect(resolveSyntaxVariant(
      { name: 'gruvbox', mode: 'dark', variant: { dark: 'gruvbox-dark-soft' } },
      { name: 'default', mode: 'light' },
      'light',
      'light',
    ).id).toBe('gruvbox-dark-soft')
  })

  it('follows the OS on a pinned palette whose mode is system', () => {
    // `system` on the syntax row means the OS, exactly as it does for the pair.
    // Reading the app's resolved mode instead would make the two rows disagree
    // about one word.
    const pref = { name: 'nord', mode: 'system' } as const
    expect(resolveSyntaxVariant(pref, { name: 'default', mode: 'light' }, 'dark', 'light').id).toBe('nord-dark')
    expect(resolveSyntaxVariant(pref, { name: 'default', mode: 'dark' }, 'light', 'dark').id).toBe('nord-light')
  })

  it('reads a stray half-sentinel the way the switch it came from would', () => {
    // The two halves move together, so this is unreachable from a parsed
    // preference -- but the function is total, and `resolveSyntaxPair` answers
    // the same way.
    expect(resolveSyntaxVariant(
      { name: 'gruvbox', mode: 'match-ui' },
      { name: 'default', mode: 'dark' },
      'light',
      'dark',
    ).id).toBe('gruvbox-dark-medium')
  })

  it.each([
    ['match-ui', { name: 'match-ui', mode: 'match-ui' } as const],
    ['a pinned dark palette', { name: 'gruvbox', mode: 'dark' } as const],
    ['a pinned light palette', { name: 'rose-pine', mode: 'light' } as const],
    ['a pinned palette on system', { name: 'nord', mode: 'system' } as const],
  ])('names a syntax theme the pair also tokenizes with, under %s', (_label, pref) => {
    // THE INVARIANT THAT TIES THE TWO. `resolveSyntaxPair` says what to tokenize
    // with; `resolveSyntaxVariant` says what the result sits on. If the variant
    // ever named a theme outside the pair, the surface would be painted for one
    // look and the tokens baked for another -- unreadable, and invisible to
    // every other case in this file.
    for (const ui of [{ name: 'catppuccin', mode: 'light' } as const, { name: 'default', mode: 'dark' } as const]) {
      for (const os of ['light', 'dark'] as const) {
        const uiMode = ui.mode === 'light' ? 'light' : 'dark'
        const pair = resolveSyntaxPair(pref, ui, os)
        const worn = resolveSyntaxVariant(pref, ui, os, uiMode)
        expect([pair.light, pair.dark], `${worn.id} is outside the pair`).toContain(worn.syntax)
      }
    }
  })
})

describe('setSyntaxTheme', () => {
  it('registers the new pair BEFORE pointing the options at it', async () => {
    const order: string[] = []
    const registrar = fakeRegistrar(order)
    // Recorded through the invalidator, which the store runs only after it has
    // switched -- so its position in `order` marks the switch.
    onSyntaxThemeChange(() => order.push('switched'))

    await setSyntaxTheme({ light: 'ayu-light', dark: 'ayu-dark' }, registrar)

    expect(order).toEqual(['register:ayu-light', 'register:ayu-dark', 'switched'])
    expect(syntaxThemePair()).toEqual({ light: 'ayu-light', dark: 'ayu-dark' })
  })

  it('bumps the generation so consumers holding tokenized output re-render', async () => {
    const before = syntaxThemeGeneration()
    await setSyntaxTheme({ light: 'one-light', dark: 'one-dark-pro' }, fakeRegistrar())
    expect(syntaxThemeGeneration()).not.toBe(before)
  })

  it('does nothing at all for a pair that is already live', async () => {
    await setSyntaxTheme({ light: 'nord-light', dark: 'nord' }, fakeRegistrar())
    const generation = syntaxThemeGeneration()
    const invalidate = vi.fn()
    onSyntaxThemeChange(invalidate)
    const registrar = fakeRegistrar()

    await setSyntaxTheme({ light: 'nord-light', dark: 'nord' }, registrar)

    // No re-register, no cache drop, no re-render: a no-op change must not
    // re-highlight every code block on screen.
    expect(registrar.themes).toEqual([])
    expect(invalidate).not.toHaveBeenCalled()
    expect(syntaxThemeGeneration()).toBe(generation)
  })

  it('drops every registered cache when it does switch', async () => {
    const first = vi.fn()
    const second = vi.fn()
    onSyntaxThemeChange(first)
    onSyntaxThemeChange(second)

    await setSyntaxTheme({ light: 'solarized-light', dark: 'solarized-dark' }, fakeRegistrar())

    expect(first).toHaveBeenCalled()
    expect(second).toHaveBeenCalled()
  })

  it('serializes overlapping changes so the last one wins', async () => {
    // Two quick changes must not interleave their register and point steps, or
    // the second could publish a pair whose themes the first had not finished
    // registering.
    const registrar = fakeRegistrar()
    const a = setSyntaxTheme({ light: 'github-light', dark: 'github-dark' }, registrar)
    const b = setSyntaxTheme({ light: 'gruvbox-light-medium', dark: 'gruvbox-dark-medium' }, registrar)
    await Promise.all([a, b])

    expect(syntaxThemePair()).toEqual({ light: 'gruvbox-light-medium', dark: 'gruvbox-dark-medium' })
    // Both pairs were registered, and the winner's halves are present.
    expect(registrar.themes).toContain('gruvbox-light-medium')
    expect(registrar.themes).toContain('gruvbox-dark-medium')
  })

  it('leaves the old pair live when the new one fails to register', async () => {
    // A chunk import can fail transiently. Switching anyway would point every
    // synchronous call site at a theme that is not there.
    const before = syntaxThemePair()
    const failing = {
      getLoadedThemes: () => [],
      loadTheme: async () => {
        throw new Error('chunk load failed')
      },
    }

    await expect(setSyntaxTheme({ light: 'everforest-light', dark: 'everforest-dark' }, failing))
      .rejects
      .toThrow('chunk load failed')

    expect(syntaxThemePair()).toEqual(before)
  })

  it('reports the failure to the caller instead of swallowing it', async () => {
    // The rejection is the ONLY signal that the code surface did not repaint.
    // Swallowing it left the user with the old colours, no console error and no
    // retry -- while `syntaxThemes` drops a rejected load precisely so a later
    // call can retry, which nothing would then have made.
    const failing = {
      getLoadedThemes: () => [],
      loadTheme: async () => {
        throw new Error('chunk load failed')
      },
    }

    await expect(setSyntaxTheme({ light: 'nord-light', dark: 'nord' }, failing))
      .rejects
      .toThrow('chunk load failed')
  })

  it('keeps serving later changes after one fails', async () => {
    // The serialized chain must not wedge on a failure: the next change has to
    // apply. A shared `.catch` on the chain is what makes this true while the
    // caller's own promise still carries the error.
    const failing = {
      getLoadedThemes: () => [],
      loadTheme: async () => {
        throw new Error('chunk load failed')
      },
    }
    await expect(setSyntaxTheme({ light: 'ayu-light', dark: 'ayu-dark' }, failing))
      .rejects
      .toThrow('chunk load failed')

    const registrar = fakeRegistrar()
    await setSyntaxTheme({ light: 'github-light', dark: 'github-dark' }, registrar)

    expect(syntaxThemePair()).toEqual({ light: 'github-light', dark: 'github-dark' })
  })
})
