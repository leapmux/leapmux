import { afterEach, describe, expect, it, vi } from 'vitest'

// Each test imports the module fresh (vi.resetModules) so the module-load-time
// `matchMedia` read re-resolves against the window state the test set up.
describe('prefersReducedMotion', () => {
  const originalMatchMedia = window.matchMedia

  afterEach(() => {
    window.matchMedia = originalMatchMedia
    vi.resetModules()
  })

  it('returns false when matchMedia is unavailable (SSR / jsdom)', async () => {
    vi.resetModules()
    // @ts-expect-error force-remove to exercise the guard path
    delete window.matchMedia
    const { prefersReducedMotion } = await import('./prefersReducedMotion')
    expect(prefersReducedMotion()).toBe(false)
  })

  it('reflects the live MediaQueryList.matches on each call', async () => {
    vi.resetModules()
    let matches = true
    // A getter so the query handle the module caches at load time reports the
    // CURRENT value, mirroring a real MediaQueryList toggling mid-session.
    window.matchMedia = ((query: string) => ({
      get matches() {
        return matches
      },
      media: query,
      onchange: null,
      addEventListener() {},
      removeEventListener() {},
      addListener() {},
      removeListener() {},
      dispatchEvent() {
        return false
      },
    })) as unknown as typeof window.matchMedia

    const { prefersReducedMotion } = await import('./prefersReducedMotion')
    expect(prefersReducedMotion()).toBe(true)

    // Flip the underlying preference: the next read reflects it without any
    // listener wiring (the value is read fresh, not cached).
    matches = false
    expect(prefersReducedMotion()).toBe(false)
  })
})
