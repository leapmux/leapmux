import type { EditorView } from '@milkdown/prose/view'
import type { HighlighterCore } from 'shiki/core'
import type { LanguageLoadResult, LazyHighlighter } from '~/lib/shikiLazyHighlighter'
import { Plugin, PluginKey } from '@milkdown/prose/state'
import { createHighlightPlugin } from 'prosemirror-highlight'
import { describe, expect, it, vi } from 'vitest'
import { createLazyOnigurumaHighlighter } from '~/lib/shikiLazyHighlighter'
import { createLazyShikiParser } from './lazyShikiParser'

/**
 * A fake LazyHighlighter whose `ensureLanguage` fails its first `failures` load
 * attempts (a transient chunk-import hiccup) and then succeeds, mirroring the real
 * highlighter's contract: concurrent calls for the same language share ONE in-flight
 * load, and a failed load drops out of in-flight so a later call retries. Backed by
 * the real Oniguruma highlighter for actual tokenizing once a load "succeeds".
 */
function createFlakyHighlighter(failures: number): LazyHighlighter {
  const real = createLazyOnigurumaHighlighter()
  let attempts = 0
  // De-dup concurrent loads of the same language into one promise, exactly as the
  // real highlighter does -- so N blocks of the same language in one pass count as a
  // single load attempt against `failures`, not N.
  const inFlight = new Map<string, Promise<LanguageLoadResult>>()
  return {
    ensureReady: () => real.ensureReady(),
    getHighlighter: () => real.getHighlighter(),
    isLanguageLoaded: lang => real.isLanguageLoaded(lang),
    ensureThemes: async () => {},
    areThemesLoaded: () => true,
    ensureLanguage: (lang) => {
      const existing = inFlight.get(lang)
      if (existing)
        return existing
      attempts += 1
      // A simulated transient chunk-import failure resolves 'failed' (not 'unsupported'),
      // exactly as the real highlighter reports a droppable/retryable load.
      const load: Promise<LanguageLoadResult> = attempts <= failures ? Promise.resolve('failed') : real.ensureLanguage(lang)
      const tracked = load.then((result) => {
        inFlight.delete(lang)
        return result
      })
      inFlight.set(lang, tracked)
      return tracked
    },
  }
}

/**
 * A fake LazyHighlighter that reports every language loaded but throws inside
 * `codeToTokens` -- a grammar that compiles yet blows up at tokenize time.
 */
function createThrowingTokenizeHighlighter(): LazyHighlighter {
  const core = {
    codeToTokens: () => {
      throw new Error('tokenize boom')
    },
  } as unknown as HighlighterCore
  return {
    ensureReady: () => Promise.resolve(core),
    getHighlighter: () => core,
    isLanguageLoaded: () => true,
    ensureThemes: async () => {},
    areThemesLoaded: () => true,
    ensureLanguage: () => Promise.resolve('loaded' as const),
  }
}

// Decoration internals: prosemirror stores the inline/node attrs on `deco.type.attrs`.
function attrsOf(deco: unknown): Record<string, unknown> {
  return (deco as { type?: { attrs?: Record<string, unknown> } })?.type?.attrs ?? {}
}

// prosemirror Decoration stores its span as `deco.from`/`deco.to`.
function rangeOf(deco: unknown): { from: number, to: number } {
  return deco as { from: number, to: number }
}

describe('createLazyShikiParser', () => {
  it('returns no decorations for an unknown language', () => {
    const parser = createLazyShikiParser(createLazyOnigurumaHighlighter())
    const result = parser({ content: 'x', language: 'not-a-language', pos: 0, size: 3 })
    expect(Array.isArray(result)).toBe(true)
    expect(result).toHaveLength(0)
  })

  it('returns no decorations when no language is set', () => {
    const parser = createLazyShikiParser(createLazyOnigurumaHighlighter())
    expect(parser({ content: 'x', language: undefined, pos: 0, size: 3 })).toEqual([])
  })

  it('returns a promise while the grammar loads, then decorations with the shiki class + dual-theme vars', async () => {
    const hl = createLazyOnigurumaHighlighter()
    const parser = createLazyShikiParser(hl)
    const content = 'const x = 1'
    const opts = { content, language: 'javascript', pos: 0, size: content.length + 2 }

    // First call: highlighter/grammar not ready yet -> a promise to re-run later.
    const first = parser(opts)
    expect(first).toBeInstanceOf(Promise)
    await first

    // Re-run once warm: synchronous decorations.
    const second = parser(opts)
    expect(Array.isArray(second)).toBe(true)
    const decos = second as unknown[]
    expect(decos.length).toBeGreaterThan(0)
    const inline = decos.map(attrsOf).find(a => a.class === 'shiki')
    expect(inline).toBeDefined()
    expect(String(inline!.style)).toContain('--shiki-light')
    expect(String(inline!.style)).toContain('--shiki-dark')
  })

  it('recovers from a transient grammar-load failure on a later re-run', async () => {
    // prosemirror-highlight re-runs the parser whenever the returned promise
    // resolves. A transient load failure (e.g. a chunk-import hiccup) must NOT
    // latch the language to plain forever for the editor mount: a later re-run
    // should retry and eventually highlight.
    const hl = createFlakyHighlighter(1)
    const parser = createLazyShikiParser(hl)
    const content = 'const x = 1'
    const opts = { content, language: 'javascript', pos: 0, size: content.length + 2 }

    // First call: load fails transiently -> a promise that resolves (no throw).
    const first = parser(opts)
    expect(first).toBeInstanceOf(Promise)
    await first

    // Second call (re-run): the parser must retry rather than render plain. The
    // grammar isn't loaded yet, so it returns another promise.
    const second = parser(opts)
    expect(second).toBeInstanceOf(Promise)
    await second

    // Third call: warm now -> synchronous highlighted decorations.
    const third = parser(opts)
    expect(Array.isArray(third)).toBe(true)
    const decos = third as unknown[]
    expect(decos.some(d => attrsOf(d).class === 'shiki')).toBe(true)
  })

  it('latches a language to plain after repeated load failures (no infinite re-run loop)', async () => {
    // A grammar that never loads must eventually render plain (return []) so the
    // block caches and prosemirror-highlight stops re-running the parser. Without
    // a bound the parser would re-await forever. Drive it past the retry cap.
    const hl = createFlakyHighlighter(Number.POSITIVE_INFINITY)
    const parser = createLazyShikiParser(hl)
    const opts = { content: 'const x = 1', language: 'javascript', pos: 0, size: 13 }

    // Keep re-running until the parser gives up and returns a plain (array) result.
    let result = parser(opts)
    let guard = 0
    while (result instanceof Promise) {
      await result
      result = parser(opts)
      guard += 1
      if (guard > 50)
        throw new Error('parser never latched to plain -- infinite re-run loop')
    }
    expect(Array.isArray(result)).toBe(true)
    expect(result).toHaveLength(0)
  })

  it('tokenizes a budget-exhausted language once the shared highlighter has loaded its grammar', async () => {
    // The `failures` retry budget is PER-parser, but the highlighter (and its `loaded`
    // set) is SHARED across every editor mount. After one mount exhausts its budget for a
    // language, another mount may still load that grammar into the shared highlighter. The
    // budget wedge must only suppress further load ATTEMPTS -- never a grammar that is now
    // actually loaded and tokenizable. (Pre-fix the wedge short-circuited before the
    // isLanguageLoaded check, latching the block to plain even after the grammar loaded.)
    const real = createLazyOnigurumaHighlighter()
    let loaded = false
    const hl: LazyHighlighter = {
      ensureReady: () => real.ensureReady(),
      getHighlighter: () => real.getHighlighter(),
      // This mount sees the grammar as absent until another mount loads it (below).
      isLanguageLoaded: () => loaded,
      ensureThemes: async () => {},
      areThemesLoaded: () => true,
      // This mount's own loads always fail transiently -- it never loads the grammar itself.
      ensureLanguage: () => Promise.resolve('failed' as const),
    }
    const parser = createLazyShikiParser(hl)
    const content = 'const x = 1'
    const opts = { content, language: 'javascript', pos: 0, size: content.length + 2 }

    // Drive this mount past its retry budget: each awaited re-run fails the load and bumps
    // `failures`, until the parser gives up and renders plain ([]).
    let result = parser(opts)
    let guard = 0
    while (result instanceof Promise) {
      await result
      result = parser(opts)
      if (++guard > 50)
        throw new Error('parser never latched to plain')
    }
    expect(result).toEqual([]) // wedged: budget exhausted, grammar not loaded

    // Another editor mount loads the grammar into the SHARED highlighter.
    await real.ensureLanguage('javascript')
    loaded = true

    // The wedged parser must now tokenize via the loaded path, NOT return [] from the budget.
    const afterLoad = parser(opts)
    expect(Array.isArray(afterLoad)).toBe(true)
    expect((afterLoad as unknown[]).some(d => attrsOf(d).class === 'shiki')).toBe(true)
  })

  it('spends one retry-budget unit per load, not one per same-language block in a pass', async () => {
    // A document with several code blocks of the SAME language all share ONE
    // de-duplicated grammar load. prosemirror-highlight invokes the parser once per
    // block in a single decoration pass, so a single transient failure must cost ONE
    // retry-budget unit -- not one per block. Otherwise >= MAX_LANG_LOAD_RETRIES (3)
    // same-language blocks would latch them all to plain after a single hiccup,
    // defeating the retry budget entirely.
    const hl = createFlakyHighlighter(1) // first shared load fails, then succeeds
    const parser = createLazyShikiParser(hl)
    const block = (pos: number) => ({ content: 'const x = 1', language: 'javascript', pos, size: 13 })

    // Pass 1: three blocks, grammar not loaded -> three promises sharing one load.
    const promises = [parser(block(0)), parser(block(20)), parser(block(40))]
    for (const p of promises)
      expect(p).toBeInstanceOf(Promise)
    await Promise.all(promises)

    // The single transient failure cost exactly ONE unit, so a re-run still RETRIES
    // (returns a promise) rather than latching to plain. With the per-block over-count
    // bug, three blocks would have pushed `failures` to 3 = MAX and this would be [].
    const retry = parser(block(0))
    expect(retry).toBeInstanceOf(Promise)
    await retry

    // The retried load succeeded -> warm -> highlighted decorations.
    const warm = parser(block(0))
    expect(Array.isArray(warm)).toBe(true)
    expect((warm as unknown[]).some(d => attrsOf(d).class === 'shiki')).toBe(true)
  })

  it('degrades a single block to plain (no throw) when a loaded grammar throws at tokenize time', () => {
    // A grammar that loads but throws inside codeToTokens must NOT propagate: the throw
    // would escape into prosemirror-highlight's calculateDecoration, whose try/catch
    // wraps the WHOLE code-block loop, dropping highlighting for every block in the pass.
    // The parser must isolate it -- return [] (this block plain) and dev-warn.
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    try {
      const parser = createLazyShikiParser(createThrowingTokenizeHighlighter())
      const result = parser({ content: 'const x = 1', language: 'javascript', pos: 0, size: 13 })
      expect(Array.isArray(result)).toBe(true)
      expect(result).toHaveLength(0)
      expect(warn).toHaveBeenCalledWith(
        expect.stringContaining('failed to tokenize'),
        expect.any(Error),
      )
    }
    finally {
      warn.mockRestore()
    }
  })

  it('keeps every decoration inside the node range for multi-line content', async () => {
    // The parser walks tokens accumulating `from` per line + 1 for each newline. A
    // multi-line block exercises that newline accounting; every inline decoration must
    // stay within (pos, pos + size) or prosemirror throws "position out of range" when
    // the decoration is applied. (Single-line content can't catch a per-line drift.)
    const hl = createLazyOnigurumaHighlighter()
    const parser = createLazyShikiParser(hl)
    const content = 'function f() {\n  return 42\n}'
    // size = textContent length + 2 (the code_block open/close tokens), matching how
    // prosemirror-highlight invokes the parser for a node.
    const opts = { content, language: 'javascript', pos: 5, size: content.length + 2 }

    await parser(opts) // warm the grammar
    const decos = parser(opts) as unknown[]
    expect(Array.isArray(decos)).toBe(true)
    expect(decos.length).toBeGreaterThan(1)
    for (const deco of decos) {
      const { from, to } = rangeOf(deco)
      expect(from).toBeGreaterThanOrEqual(opts.pos)
      expect(to).toBeLessThanOrEqual(opts.pos + opts.size)
      expect(to).toBeGreaterThanOrEqual(from)
    }
    // The inline decorations together cover all three lines' content (multi-line walk).
    const inlineDecos = decos.filter(d => attrsOf(d).class === 'shiki')
    expect(inlineDecos.length).toBeGreaterThanOrEqual(3)
  })
})

describe('refreshEditorHighlight', () => {
  // The plugin must be REPLACED, not merely refreshed. prosemirror-highlight
  // keys its DecorationCache on the node and reads through it, so the supported
  // refresh meta recomputes nothing for a block whose node did not change --
  // which is every block when only the theme moved.
  it('swaps the highlight plugin for a fresh instance', async () => {
    const { refreshEditorHighlight } = await import('~/components/chat/markdownEditor/editorSetup')

    const highlight = createHighlightPlugin({ parser: () => [] })
    const other = new Plugin({ key: new PluginKey('unrelated') })
    let reconfigured: readonly Plugin[] | undefined
    const view = {
      composing: false,
      state: {
        plugins: [other, highlight],
        reconfigure: ({ plugins }: { plugins: readonly Plugin[] }) => {
          reconfigured = plugins
          return { plugins }
        },
      },
      updateState: () => {},
    } as unknown as EditorView

    refreshEditorHighlight(view)

    expect(reconfigured).toBeDefined()
    expect(reconfigured![0]).toBe(other)
    expect(reconfigured![1]).not.toBe(highlight)
  })

  // An IME composition is the one case a rebuild must not interrupt, so the
  // guard DEFERS the repaint over the composition and re-runs it at
  // `compositionend`.
  it('defers the repaint while an IME composition is in flight, then runs it', async () => {
    const { refreshEditorHighlight } = await import('~/components/chat/markdownEditor/editorSetup')

    let reconfigured = 0
    const dom = document.createElement('div')
    const view = {
      composing: true,
      dom,
      state: {
        plugins: [createHighlightPlugin({ parser: () => [] })],
        reconfigure: () => {
          reconfigured += 1
          return {}
        },
      },
      updateState: () => {},
    } as unknown as EditorView

    refreshEditorHighlight(view)
    expect(reconfigured).toBe(0)

    // The composition ends, and the repaint the guard deferred runs.
    //
    // Dropping it was wrong: `prosemirror-highlight` keys its DecorationCache on
    // the NODE, so the composition-ending transaction recomputes only the block
    // the composition touched. Every OTHER code block in the composer kept the
    // abandoned theme's baked colours for the rest of the session.
    ;(view as unknown as { composing: boolean }).composing = false
    dom.dispatchEvent(new Event('compositionend'))
    expect(reconfigured).toBe(1)

    // ONCE. A second `compositionend` with no new theme change must not repaint
    // again -- the listener is registered `{ once: true }`.
    dom.dispatchEvent(new Event('compositionend'))
    expect(reconfigured).toBe(1)
  })
})

describe('lazyShikiParser theme registration failure', () => {
  // The branch every fake in this file stubbed past: `ensureThemes: async () => {}`
  // and `areThemesLoaded: () => true` meant no test ever reached it.
  //
  // `ensureThemes` REJECTS when a lazy theme chunk fails to import, which is an
  // ordinary event -- a 404 after a deploy, an offline tab. A rejected promise
  // handed to `prosemirror-highlight` is logged and dropped WITHOUT a refresh,
  // so the block stays uncached and every later keystroke fires another failing
  // import with another console error. The grammar branch beside it has a
  // budget for exactly this; the theme branch had none.
  function failingHighlighter(attempts: { n: number }) {
    return {
      getHighlighter: () => ({
        codeToTokens: () => ({ tokens: [[]], rootStyle: '' }),
      }),
      isLanguageLoaded: () => true,
      ensureLanguage: async () => 'loaded' as const,
      areThemesLoaded: () => false,
      ensureThemes: async () => {
        attempts.n += 1
        throw new Error('chunk 404')
      },
      ensureReady: async () => ({}),
    } as unknown as Parameters<typeof createLazyShikiParser>[0]
  }

  it('gives up after a bounded number of attempts instead of retrying forever', async () => {
    const attempts = { n: 0 }
    const parse = createLazyShikiParser(failingHighlighter(attempts))
    const opts = { content: 'const x = 1', language: 'ts', pos: 0, size: 13 }

    // Each pass returns the load; awaiting it is what a re-run does.
    for (let i = 0; i < 10; i++) {
      const result = parse(opts)
      if (Array.isArray(result))
        break
      await result
    }

    // Bounded. Without the budget this climbed with every keystroke.
    expect(attempts.n).toBeLessThanOrEqual(3)
    // And it settles on a PLAIN render, which the plugin caches -- that is what
    // ends the resolve-and-re-run loop.
    expect(parse(opts)).toEqual([])
  })

  it('does not reject, so the plugin refreshes instead of logging and dropping', async () => {
    const attempts = { n: 0 }
    const parse = createLazyShikiParser(failingHighlighter(attempts))
    const result = parse({ content: 'const x = 1', language: 'ts', pos: 0, size: 13 })

    expect(Array.isArray(result)).toBe(false)
    // A rejection here is the defect: prosemirror-highlight logs it and does
    // NOT call refresh(), so the block never recovers and never caches.
    await expect(result as Promise<void>).resolves.toBeUndefined()
  })
})
