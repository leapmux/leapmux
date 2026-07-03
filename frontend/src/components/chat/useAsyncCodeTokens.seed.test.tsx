import type { CachedToken } from '~/lib/tokenCache'
import { createRoot } from 'solid-js'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { _resetTokenCache, setCachedTokens } from '~/lib/tokenCache'
import { useAsyncCodeTokens } from './useAsyncCodeTokens'

// Real token cache; only the worker client is stubbed (a never-resolving dispatch) so a
// genuine cache MISS would render plain forever -- proving the highlighted first frame
// comes from the synchronous seed, not a worker round-trip.
vi.mock('~/lib/shikiWorkerClient', () => ({
  tokenizeAsync: vi.fn(() => new Promise<CachedToken[][] | null>(() => {})),
}))

const TOKENS: CachedToken[][] = [[{ content: '{', className: 'sk-test' }]]

// Read the hook's value in the SAME synchronous tick as its creation -- before any
// createEffect has run -- which is exactly what a component commits on its first render.
function firstFrame(opts: Parameters<typeof useAsyncCodeTokens>[0]): CachedToken[][] | null {
  let frame: CachedToken[][] | null = null
  createRoot((dispose) => {
    const tokens = useAsyncCodeTokens(opts)
    frame = tokens()
    dispose()
  })
  return frame
}

describe('useAsyncCodeTokens synchronous cache seed', () => {
  afterEach(() => {
    _resetTokenCache()
    vi.clearAllMocks()
  })

  it('paints cached tokens on the first frame of a warm re-mount (no plain flash)', async () => {
    setCachedTokens('json', '{"a":1}', TOKENS)
    const { tokenizeAsync } = await import('~/lib/shikiWorkerClient')
    expect(firstFrame({
      lang: () => 'json',
      code: () => '{"a":1}',
      eligible: () => true,
      gate: () => ({ premeasure: false, hold: false }),
    })).toEqual(TOKENS)
    // The seed served it -- no worker dispatch was needed.
    expect(tokenizeAsync).not.toHaveBeenCalled()
  })

  it('renders plain on the first frame when the cache misses (cold mount)', () => {
    expect(firstFrame({
      lang: () => 'json',
      code: () => '{"a":1}',
      eligible: () => true,
      gate: () => ({ premeasure: false, hold: false }),
    })).toBeNull()
  })

  it('does not seed while premeasuring (geometry-only render stays plain)', () => {
    setCachedTokens('json', '{"a":1}', TOKENS)
    expect(firstFrame({
      lang: () => 'json',
      code: () => '{"a":1}',
      eligible: () => true,
      gate: () => ({ premeasure: true, hold: false }),
    })).toBeNull()
  })

  it('seeds cached tokens THROUGH the hold gate (a fresh mount paints highlighted even while paused)', () => {
    // The click that expands/collapses a row (or toggles the diff view) fires a pointerdown
    // that pauses syntax highlighting (hold=true) for a scroll-idle beat. A fresh mount's
    // first paint is not a disruptive text-node swap, so the seed must still apply the cached
    // tokens -- otherwise the body flashes plain until the pause lifts. This is the fix for
    // the second-view (cached) flash; only `premeasure` blocks the seed.
    setCachedTokens('json', '{"a":1}', TOKENS)
    expect(firstFrame({
      lang: () => 'json',
      code: () => '{"a":1}',
      eligible: () => true,
      gate: () => ({ premeasure: false, hold: true }),
    })).toEqual(TOKENS)
  })

  it('seeds from the synchronous tokenizer (ANSI) too', () => {
    expect(firstFrame({
      lang: () => 'ansi',
      code: () => 'x',
      eligible: () => true,
      gate: () => ({ premeasure: false, hold: false }),
      syncTokenize: () => TOKENS,
    })).toEqual(TOKENS)
  })
})
