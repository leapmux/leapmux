import { render, waitFor } from '@solidjs/testing-library'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { PreferencesProvider, usePreferences } from '~/context/PreferencesContext'
import { KEY_BROWSER_PREFS, loadBrowserPrefs } from '~/lib/browserStorage'

// Mock the user preferences API to avoid hitting a real network and to keep
// account-level fields at their hardcoded defaults during tests.
vi.mock('~/api/clients', () => ({
  userClient: {
    getPreferences: vi.fn().mockResolvedValue({}),
    updatePreferences: vi.fn().mockResolvedValue({}),
  },
  authClient: {},
}))

type Prefs = ReturnType<typeof usePreferences>

function captureContext(): { get: () => Prefs } {
  let prefs: Prefs | undefined
  function Capture() {
    prefs = usePreferences()
    return null as unknown as ReturnType<typeof usePreferences>
  }
  render(() => (
    <PreferencesProvider>
      <Capture />
    </PreferencesProvider>
  ))
  return {
    get: () => {
      if (!prefs)
        throw new Error('Preferences context not yet captured')
      return prefs
    },
  }
}

beforeEach(() => {
  localStorage.clear()
  // Clear the global theme setter that PreferencesContext.setBrowserTheme calls.
  ;(window as unknown as { __leapmux_setTheme?: (v: string) => void }).__leapmux_setTheme = undefined
})

afterEach(() => {
  localStorage.clear()
})

describe('preferencesContext — browser-level theme override', () => {
  it('starts with no browser-level override when localStorage is empty', () => {
    const ctx = captureContext()
    expect(ctx.get().browserTheme()).toBeNull()
    // Theme should resolve to the hardcoded account default.
    expect(ctx.get().theme()).toBe('system')
  })

  it('persists a browser-level dark theme to localStorage', () => {
    const ctx = captureContext()
    ctx.get().setBrowserTheme('dark')

    expect(ctx.get().browserTheme()).toBe('dark')
    expect(ctx.get().theme()).toBe('dark')
    expect(loadBrowserPrefs().theme).toBe('dark')
  })

  it('persists a browser-level light theme to localStorage', () => {
    const ctx = captureContext()
    ctx.get().setBrowserTheme('light')

    expect(ctx.get().browserTheme()).toBe('light')
    expect(loadBrowserPrefs().theme).toBe('light')
  })

  it('clearing the browser theme removes the key from the consolidated prefs blob', () => {
    const ctx = captureContext()
    ctx.get().setBrowserTheme('dark')
    expect(loadBrowserPrefs().theme).toBe('dark')

    ctx.get().setBrowserTheme(null)
    expect(ctx.get().browserTheme()).toBeNull()
    // The serialized blob should not have a `theme` field after clearing.
    expect('theme' in loadBrowserPrefs()).toBe(false)
  })

  it('falls back to account default once the browser override is cleared', () => {
    const ctx = captureContext()
    ctx.get().setBrowserTheme('dark')
    expect(ctx.get().theme()).toBe('dark')

    ctx.get().setBrowserTheme(null)
    expect(ctx.get().theme()).toBe('system')
  })

  it('hydrates the browser theme from localStorage on provider mount (simulated reload)', () => {
    // Pre-seed localStorage with a stored preference and mount fresh.
    localStorage.setItem(KEY_BROWSER_PREFS, JSON.stringify({ theme: 'dark' }))
    const ctx = captureContext()
    expect(ctx.get().browserTheme()).toBe('dark')
    expect(ctx.get().theme()).toBe('dark')
  })

  it('notifies the global theme setter when set', () => {
    const setter = vi.fn()
    ;(window as unknown as { __leapmux_setTheme: (v: string) => void }).__leapmux_setTheme = setter

    const ctx = captureContext()
    ctx.get().setBrowserTheme('dark')
    expect(setter).toHaveBeenCalledWith('dark')

    ctx.get().setBrowserTheme(null)
    // null should fall back to account default ("system" by default).
    expect(setter).toHaveBeenLastCalledWith('system')
  })
})

describe('preferencesContext — browser-level diff view override', () => {
  it('starts with no browser-level override and resolves to the account default', () => {
    const ctx = captureContext()
    expect(ctx.get().browserDiffView()).toBeNull()
    expect(ctx.get().diffView()).toBe('unified')
  })

  it('round-trips browser-level "unified" through localStorage', () => {
    const ctx = captureContext()
    ctx.get().setBrowserDiffView('unified')
    expect(ctx.get().browserDiffView()).toBe('unified')
    expect(loadBrowserPrefs().diffView).toBe('unified')
    expect(ctx.get().diffView()).toBe('unified')
  })

  it('round-trips browser-level "split" through localStorage', () => {
    const ctx = captureContext()
    ctx.get().setBrowserDiffView('split')
    expect(ctx.get().browserDiffView()).toBe('split')
    expect(loadBrowserPrefs().diffView).toBe('split')
    expect(ctx.get().diffView()).toBe('split')
  })

  it('clearing the browser diff view removes the key from the consolidated prefs blob', () => {
    const ctx = captureContext()
    ctx.get().setBrowserDiffView('split')
    expect(loadBrowserPrefs().diffView).toBe('split')

    ctx.get().setBrowserDiffView(null)
    expect(ctx.get().browserDiffView()).toBeNull()
    expect('diffView' in loadBrowserPrefs()).toBe(false)
  })

  it('hydrates the browser diff view from localStorage on provider mount', () => {
    localStorage.setItem(KEY_BROWSER_PREFS, JSON.stringify({ diffView: 'split' }))
    const ctx = captureContext()
    expect(ctx.get().browserDiffView()).toBe('split')
    expect(ctx.get().diffView()).toBe('split')
  })
})

describe('preferencesContext — multiple prefs in one blob', () => {
  it('writes multiple browser overrides to a single consolidated key', () => {
    const ctx = captureContext()
    ctx.get().setBrowserTheme('dark')
    ctx.get().setBrowserDiffView('split')
    ctx.get().setBrowserTurnEndSound('none')

    const prefs = loadBrowserPrefs()
    expect(prefs.theme).toBe('dark')
    expect(prefs.diffView).toBe('split')
    expect(prefs.turnEndSound).toBe('none')
  })

  it('clearing one pref does not clear the others', () => {
    const ctx = captureContext()
    ctx.get().setBrowserTheme('dark')
    ctx.get().setBrowserDiffView('split')

    ctx.get().setBrowserDiffView(null)
    const prefs = loadBrowserPrefs()
    expect(prefs.theme).toBe('dark')
    expect('diffView' in prefs).toBe(false)
  })
})

describe('preferencesContext — reload from API', () => {
  it('runs reload() on mount without throwing when the API returns no preferences', async () => {
    // The default mock returns `{}` (no `preferences` field). Provider should
    // tolerate that without throwing and signal values should remain at defaults.
    const ctx = captureContext()
    await waitFor(() => {
      expect(ctx.get().theme()).toBe('system')
    })
  })
})
