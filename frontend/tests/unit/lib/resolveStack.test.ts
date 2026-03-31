import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { resolveStack } from '~/lib/resolveStack'

// Use distinct URLs per test to avoid the consumer cache conflicting.

describe('resolveStack', () => {
  beforeEach(() => {
    vi.stubGlobal('fetch', vi.fn())
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('should resolve minified stack frames using source maps', async () => {
    // Generated line 11, col 5 → source "src/lib/channel.ts", line 10, col 5, name "openChannel"
    // Mappings string produced by SourceMapGenerator for this single mapping.
    const sourceMap = {
      version: 3,
      file: 'app-resolve.js',
      sources: ['src/lib/channel.ts'],
      names: ['openChannel'],
      mappings: ';;;;;;;;;;KASKA',
    }

    vi.mocked(fetch).mockResolvedValue(
      new Response(JSON.stringify(sourceMap), { status: 200 }),
    )

    const stack = `Error: something broke
    at http://localhost:4327/_build/assets/app-resolve.js:11:5`

    const result = await resolveStack(stack)
    expect(result).toContain('channel.ts')
    expect(result).toContain('openChannel')
    // First line (error message) should be preserved as-is.
    expect(result).toMatch(/^Error: something broke/)
  })

  it('should return original frame when source map is not available', async () => {
    vi.mocked(fetch).mockResolvedValue(new Response('', { status: 404 }))

    const stack = `Error: test
    at http://localhost:4327/_build/assets/missing.js:10:20`

    const result = await resolveStack(stack)
    expect(result).toBe(stack)
  })

  it('should preserve non-frame lines', async () => {
    vi.mocked(fetch).mockResolvedValue(new Response('', { status: 404 }))

    const stack = `TypeError: Cannot read properties of undefined
    custom context line
    at http://localhost:4327/nomatch.js:1:1`

    const result = await resolveStack(stack)
    expect(result).toContain('TypeError: Cannot read properties of undefined')
    expect(result).toContain('custom context line')
  })

  it('should handle fetch errors gracefully', async () => {
    vi.mocked(fetch).mockRejectedValue(new Error('network error'))

    const stack = `Error: test
    at http://localhost:4327/_build/assets/app-fetch-err.js:5:10`

    const result = await resolveStack(stack)
    expect(result).toBe(stack)
  })

  it('should parse Safari-style stack frames', async () => {
    const sourceMap = {
      version: 3,
      file: 'safari-bundle.js',
      sources: ['src/app.ts'],
      names: ['init'],
      mappings: ';;;;;;;;;;KASKA',
    }

    vi.mocked(fetch).mockResolvedValue(
      new Response(JSON.stringify(sourceMap), { status: 200 }),
    )

    const stack = `init@http://localhost:4327/_build/assets/safari-bundle.js:11:5`
    const result = await resolveStack(stack)
    expect(result).toContain('app.ts')
  })
})
