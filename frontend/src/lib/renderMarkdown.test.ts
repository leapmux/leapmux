import { createEffect, createRoot } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { DEFAULT_THEME_ID } from '~/styles/themes'
import { settleWorkerReplies } from '~/test-support/fakeWorker'
import { raiseStackOverflowFor } from '~/test-support/markdownParseOverflow'
import { plainMarkdownProcessor } from './markdownProcessor'
import { _getMarkdownCacheSize, _resetMarkdownCache, requestMarkdownHtml } from './renderMarkdown'
import { _resetShikiStyleClassesForTest } from './shikiStyleClass'
import { readInjectedShikiRules } from './shikiStyleClass.testkit'
import { shikiHighlighter } from './shikiSyncHighlighter'
import { syntaxPairFor } from './syntaxThemes'
import { setSyntaxTheme } from './syntaxThemeStore'

// ---------------------------------------------------------------------------
// renderMarkdown with the real Markdown worker
//
// The stand-in Worker of the jsdom tests runs the real worker's handler (see
// ~/test-support/fakeWorker), so each body renders through the same pipeline as
// in the browser. A worker that the test answers by hand is in
// `renderMarkdown.heldWorker.test.ts`.
// ---------------------------------------------------------------------------

/** The HTML of `text`, which is pending until the worker that renders it answers. */
async function renderedHtml(text: string): Promise<string | undefined> {
  expect(requestMarkdownHtml(text)).toBeUndefined()
  await settleWorkerReplies()
  return requestMarkdownHtml(text)
}

describe('renderMarkdown shared token-style classes', () => {
  beforeEach(() => {
    _resetMarkdownCache()
    _resetShikiStyleClassesForTest()
  })

  it('renders fenced code with class-based token spans and injects the rules', async () => {
    const html = await renderedHtml('```js\nconst x = 1\n```') ?? ''
    // Token spans carry shared classes, never inline styles. (The <pre> keeps
    // its per-block rootStyle -- one element -- so scope the assertion to spans.)
    expect(html).toContain('class="sk-')
    expect(html).not.toContain('<span style=')
    // Every class referenced by the HTML has an injected rule defining the
    // dual-theme variables.
    const classes = [
      ...html.matchAll(/class="(sk-[0-9a-z-]+)"/g),
    ].map(m => m[1])
    expect(classes.length).toBeGreaterThan(0)
    for (const className of new Set(classes))
      expect(readInjectedShikiRules()).toContain(`.${className}{`)
    expect(readInjectedShikiRules()).toContain('--shiki-light')
    expect(readInjectedShikiRules()).toContain('--shiki-dark')
  })
})

describe('renderMarkdown syntax theme invalidation', () => {
  beforeEach(() => {
    _resetMarkdownCache()
  })

  it('bumps the version so a consumer that reads only markdownVersion re-renders', async () => {
    // Every other mutation of these caches pairs the clear with a version bump.
    // The syntax-theme invalidator did not, and a chat row survived only because
    // its own cache namespace folds in `syntaxThemeGeneration()`. A consumer that
    // calls requestMarkdownHtml alone depends on the text and on `markdownVersion`
    // and nothing else, so it kept the abandoned theme's baked token colours
    // against a code surface already repainted for the new one.
    const text = '# heading'
    let runs = 0
    const dispose = createRoot((disposeRoot) => {
      createEffect(() => {
        requestMarkdownHtml(text)
        runs += 1
      })
      return disposeRoot
    })
    // The render lands first, which re-runs the consumer by itself.
    await settleWorkerReplies()
    expect(requestMarkdownHtml(text)).toContain('heading')
    const before = runs
    expect(before).toBeGreaterThan(0)

    // Point the store at the OTHER pair, which runs the invalidators.
    const other = syntaxPairFor('nord')
    await setSyntaxTheme(other, shikiHighlighter)
    // The bump is coalesced through a microtask.
    await new Promise(resolve => queueMicrotask(() => resolve(null)))
    await Promise.resolve()

    expect(runs).toBeGreaterThan(before)

    // Put the module state back for the rest of the file.
    await setSyntaxTheme(syntaxPairFor(DEFAULT_THEME_ID), shikiHighlighter)
    dispose()
    await settleWorkerReplies()
  })
})

describe('renderMarkdown cache and gfm', () => {
  beforeEach(() => {
    _resetMarkdownCache()
  })

  it('should render markdown with syntax highlighting', async () => {
    const html = await renderedHtml('```js\nconst x = 1\n```')
    expect(html).toContain('class="shiki')
    expect(html).toContain('const')
    // Tokenized: the keyword sits in a span of its own.
    expect(html).not.toContain('const x = 1')
  })

  it('should render plain text', async () => {
    const html = await renderedHtml('hello world')
    expect(html).toContain('hello world')
  })

  it('should render inline code', async () => {
    const html = await renderedHtml('use `const x = 1`')
    expect(html).toContain('<code>')
    expect(html).toContain('const x = 1')
  })

  it('should render code blocks with unknown languages without crashing', async () => {
    // Unknown language should fall back gracefully (no Shiki highlighting).
    const html = await renderedHtml('```unknownlang123\nfoo bar\n```')
    expect(html).toContain('foo bar')
  })

  it('should render GFM tables', async () => {
    const md = '| a | b |\n|---|---|\n| 1 | 2 |'
    const html = await renderedHtml(md)
    expect(html).toContain('<table>')
    expect(html).toContain('<td>')
  })

  it('should cache results', async () => {
    expect(_getMarkdownCacheSize()).toBe(0)
    const html1 = await renderedHtml('cached test')
    expect(_getMarkdownCacheSize()).toBe(1)
    const html2 = requestMarkdownHtml('cached test')
    // Second call must hit the cache -- size stays at 1, not 2.
    expect(_getMarkdownCacheSize()).toBe(1)
    expect(html1).toBe(html2)
  })

  it('returns an empty body as empty HTML without caching it', () => {
    expect(requestMarkdownHtml('')).toBe('')
    expect(_getMarkdownCacheSize()).toBe(0)
  })

  it('answers a body whose parse overflows the stack with its escaped source, parsing it once', async () => {
    // Every unified processor raises the RangeError of an overflow for this body.
    const text = '> overflowing <b>deep</b>'
    const processSync = raiseStackOverflowFor(plainMarkdownProcessor, text)
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    try {
      const html = await renderedHtml(text)

      expect(html).toContain('data-markdown-source-fallback')
      expect(html).toContain('&#x3C;b>deep&#x3C;/b>')
      expect(html).not.toContain('<b>')
      // The plain processor shares the parser, so a second parse could only fail again.
      expect(processSync.mock.calls.filter(([value]) => value === text)).toHaveLength(1)
    }
    finally {
      processSync.mockRestore()
      warn.mockRestore()
    }
  })
})
