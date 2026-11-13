import type { MarkdownRenderContext } from './renderContext'
import { render } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it } from 'vitest'
import { _resetTokenCache, setCachedTokens, toCachedTokens } from '~/lib/tokenCache'
import { recordWorkerRequests, settleWorkerReplies } from '~/test-support/fakeWorker'
import { CommandHighlightHtml, JsonHighlightHtml } from './syntaxHighlight'

const pausedContext: MarkdownRenderContext = {
  syntaxHighlightingPaused: () => true,
}

/** The language and the code of each tokenize request that reaches the Shiki worker from now on. */
function recordTokenizeRequests(): () => Array<[string, string]> {
  const requests = recordWorkerRequests('lib/shikiWorker.ts')
  return () => requests.map(({ lang, code }) => [lang, code])
}

/** The text of each token span, in document order. */
function tokenTexts(container: HTMLElement): string[] {
  return [
    ...container.querySelectorAll('[data-shiki-token]'),
  ].map(token => token.textContent ?? '')
}

beforeEach(() => {
  // Reset the shared token cache.
  // Tokens from another test must not suppress the required dispatch.
  _resetTokenCache()
})

describe('json/bash async token highlighting', () => {
  it('dispatches eligible JSON to the worker and renders token spans', async () => {
    const tokenized = recordTokenizeRequests()

    const { container } = render(() => <JsonHighlightHtml code={'{"a":1}'} />)
    await settleWorkerReplies()

    expect(tokenized()).toEqual([
      ['json', '{"a":1}'],
    ])
    expect(tokenTexts(container).join('')).toBe('{"a":1}')
    expect(tokenTexts(container)).toContain('"a"')
    // Each token span carries its shared style class (see shikiStyleClass).
    for (const token of container.querySelectorAll('[data-shiki-token]'))
      expect(token.className).toMatch(/^sk-/)
  })

  it('dispatches eligible Bash with the bash language', async () => {
    const tokenized = recordTokenizeRequests()

    const { container } = render(() => <CommandHighlightHtml code="echo hi" />)
    await settleWorkerReplies()

    expect(tokenized()).toEqual([
      ['bash', 'echo hi'],
    ])
    expect(tokenTexts(container)).toContain('echo')
  })

  it('serves cached tokens synchronously without dispatching to the worker', () => {
    const tokenized = recordTokenizeRequests()
    const cached = toCachedTokens([
      [
        {
          content: '{',
          htmlStyle: { color: 'rgb(4, 5, 6)' },
        },
      ],
    ])

    setCachedTokens('json', '{"a":1}', cached)

    const { container } = render(() => <JsonHighlightHtml code={'{"a":1}'} />)

    // One token was cached above; `?.` is the type-level guard alone.
    expect(container.querySelector(`.${cached[0]?.[0]?.className ?? ''}`)).not.toBeNull()
    expect(tokenized()).toEqual([])
  })

  it('does not dispatch oversized JSON (over the char cap) and shows raw text', async () => {
    // JSON and Bash use the same highlight limit.
    const tokenized = recordTokenizeRequests()
    const huge = `{"a":"${'x'.repeat(20001)}"}` // > 20000 chars

    const { container } = render(() => <JsonHighlightHtml code={huge} />)
    await settleWorkerReplies()

    expect(tokenized()).toEqual([])
    expect(container.querySelector('[data-shiki-token]')).toBeNull()
    expect(container.textContent).toContain('xxxxx') // raw JSON text still rendered
  })

  it('does not dispatch empty code (nothing to tokenize)', async () => {
    // Empty text requires no worker dispatch.
    // Size eligibility admits zero characters, so currentKey must refuse that dispatch.
    const tokenized = recordTokenizeRequests()

    const { container } = render(() => <JsonHighlightHtml code="" />)
    await settleWorkerReplies()

    expect(tokenized()).toEqual([])
    expect(container.querySelector('[data-shiki-token]')).toBeNull()
  })

  it('does not dispatch while syntax highlighting is paused', async () => {
    const tokenized = recordTokenizeRequests()

    const { container } = render(() => <JsonHighlightHtml code={'{"a":1}'} context={pausedContext} />)
    await settleWorkerReplies()

    expect(tokenized()).toEqual([])
    expect(container.querySelector('[data-shiki-token]')).toBeNull()
  })
})

describe('JsonHighlightHtml output ownership', () => {
  it('marks raw JSON output while token rendering is paused', () => {
    const { container } = render(() => <JsonHighlightHtml dataToolOutputPreview code={'{"count":0}'} context={pausedContext} />)
    expect(container.firstElementChild?.getAttribute('data-tool-output-preview')).toBe('')
    expect(container.textContent).toBe('{"count":0}')
  })

  it('keeps ownership on the same host when tokenized JSON arrives', async () => {
    const { container } = render(() => <JsonHighlightHtml dataToolOutputPreview code={'{"count":0}'} />)
    const host = container.firstElementChild
    expect(container.querySelector('[data-shiki-token]')).toBeNull()
    await settleWorkerReplies()
    expect(container.querySelector('[data-shiki-token]')).not.toBeNull()
    expect(container.firstElementChild).toBe(host)
    expect(host?.getAttribute('data-tool-output-preview')).toBe('')
    expect(host?.textContent).toBe('{"count":0}')
  })

  it.each([false, undefined])('leaves ordinary JSON unmarked for ownership %j', (dataToolOutputPreview) => {
    const { container } = render(() => (
      <JsonHighlightHtml
        {...(dataToolOutputPreview !== undefined
          ? { dataToolOutputPreview }
          : {})}
        code={'{"count":0}'}
        context={pausedContext}
      />
    ))
    expect(container.querySelector('[data-tool-output-preview]')).toBeNull()
  })

  it('does not mark command input that repeats an output marker', () => {
    const { container } = render(() => <CommandHighlightHtml code="printf native-output-marker" context={pausedContext} />)
    expect(container.textContent).toBe('printf native-output-marker')
    expect(container.querySelector('[data-tool-output-preview]')).toBeNull()
  })
})
