import { describe, expect, it } from 'vitest'
import { renderWithPlainFallback } from '~/lib/markdownProcessor'

type Processor = Parameters<typeof renderWithPlainFallback>[0]

describe('renderWithPlainFallback', () => {
  it('returns the processor output when it succeeds', () => {
    const ok = { processSync: (t: string) => `<pre class="shiki">${t}</pre>` } as unknown as Processor
    expect(renderWithPlainFallback(ok, 'const x = 1')).toContain('class="shiki"')
  })

  it('degrades to a plain (un-highlighted) render when the processor throws', () => {
    // Shiki's regex engine can throw on certain grammars; the fallback must still render
    // the body (un-highlighted) rather than propagate -- this is the single-sourced rule
    // both the main-thread sync path and the worker rely on.
    const throwing = {
      processSync: () => {
        throw new Error('shiki regex boom')
      },
    } as unknown as Processor
    const html = renderWithPlainFallback(throwing, '```js\nconst x = 1\n```')
    expect(html).toContain('const x = 1')
    expect(html).not.toContain('class="shiki')
  })
})
