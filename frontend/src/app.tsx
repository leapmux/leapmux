import type { ParentComponent } from 'solid-js'
import { Router } from '@solidjs/router'
import { FileRoutes } from '@solidjs/start/router'
import { createEffect, createSignal, onCleanup, onMount, Show, Suspense } from 'solid-js'
import { isWailsApp } from '~/api/desktopBridge'
import { channelManager } from '~/api/workerRpc'
import { LauncherView } from '~/components/desktop/LauncherView'
import { UserMenuDialogs } from '~/components/shell/UserMenu'
import { AuthProvider } from '~/context/AuthContext'
import { PreferencesProvider, usePreferences } from '~/context/PreferencesContext'
import { disableTextSubstitutions } from '~/lib/textInputBehavior'
import { heightFull } from '~/styles/shared.css'
import '~/lib/oat'
import '@knadh/oat/oat.min.css'
import '@knadh/oat/oat.min.js'
import './styles/dropdown-flip.css'
import './styles/global.css'

export type ThemePreference = 'light' | 'dark' | 'system'

/** Read the saved theme preference from localStorage (instant, no flash). */
function getStoredTheme(): ThemePreference {
  const stored = localStorage.getItem('leapmux-theme')
  if (stored === 'light' || stored === 'dark' || stored === 'system')
    return stored
  return 'system' // 'account-default' or missing → default until account loads
}

/** Resolve the effective theme based on preference + system setting. */
function resolveTheme(pref: ThemePreference): 'light' | 'dark' {
  if (pref === 'light')
    return 'light'
  if (pref === 'dark')
    return 'dark'
  // system
  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
}

/**
 * Syncs the resolved theme and font preferences from PreferencesContext
 * to the app-level theme signal and DOM.
 */
const PreferencesApplier: ParentComponent = (props) => {
  const preferences = usePreferences()

  // When the resolved theme changes (e.g. account data loaded), push to app signal.
  createEffect(() => {
    const setter = (window as any).__leapmux_setTheme
    if (setter)
      setter(preferences.theme())
  })

  return (
    <div style={{ 'height': '100%', 'font-family': preferences.uiFontFamily() }}>
      {props.children}
    </div>
  )
}

/**
 * Wraps app content in desktop mode to prevent a brief flash of
 * "Loading..." from AuthGuard while auth is resolving. Starts at
 * opacity 0 and fades in after a short delay.
 */
const DesktopFadeIn: ParentComponent = (props) => {
  const [opacity, setOpacity] = createSignal(isWailsApp() ? 0 : 1)

  onMount(() => {
    if (!isWailsApp())
      return
    // Delay slightly to let auth resolve before fading in.
    const timer = setTimeout(setOpacity, 150, 1)
    onCleanup(() => clearTimeout(timer))
  })

  return (
    <div style={{ height: '100%', opacity: opacity(), transition: 'opacity 0.3s ease' }}>
      {props.children}
    </div>
  )
}

export default function App() {
  const [desktopConnected, setDesktopConnected] = createSignal(!isWailsApp())
  // Expose so UserMenu's "Switch mode..." can reset without page reload.
  // Wails doesn't re-inject window.go after reload, so we switch in-place.
  ;(window as any).__leapmux_disconnectDesktop = () => {
    // Close all cached channels and the WebSocket relay so the new
    // Hub instance starts with a clean slate.
    channelManager.closeAll()
    setDesktopConnected(false)
  }

  const [themePreference, setThemePreference] = createSignal<ThemePreference>(getStoredTheme())
  const [resolvedTheme, setResolvedTheme] = createSignal(resolveTheme(getStoredTheme()))

  // Listen for system theme changes when preference is 'system'.
  createEffect(() => {
    const pref = themePreference()
    setResolvedTheme(resolveTheme(pref))

    if (pref === 'system') {
      const mq = window.matchMedia('(prefers-color-scheme: dark)')
      const handler = () => setResolvedTheme(resolveTheme('system'))
      mq.addEventListener('change', handler)
      onCleanup(() => mq.removeEventListener('change', handler))
    }
  })

  // Apply Oat theme via data-theme attribute on <html>.
  // Also update the PWA theme-color meta tag.
  createEffect(() => {
    const theme = resolvedTheme()
    if (theme === 'dark') {
      document.documentElement.setAttribute('data-theme', 'dark')
    }
    else {
      document.documentElement.removeAttribute('data-theme')
    }
    const meta = document.querySelector('meta[name="theme-color"]')
    if (meta) {
      meta.setAttribute('content', theme === 'dark' ? '#1a1917' : '#ffffff')
    }
  })

  // Listen for localStorage changes from other tabs.
  const handleStorage = (e: StorageEvent) => {
    if (e.key === 'leapmux-theme') {
      const val = e.newValue
      if (val === 'light' || val === 'dark' || val === 'system')
        setThemePreference(val)
      else
        setThemePreference('system') // 'account-default' or null
    }
  }
  window.addEventListener('storage', handleStorage)
  onCleanup(() => window.removeEventListener('storage', handleStorage))

  // Expose setter for PreferencesContext/PreferencesApplier to update app theme.
  // Does NOT write to localStorage — callers handle storage themselves.
  ;(window as any).__leapmux_setTheme = (pref: ThemePreference) => {
    setThemePreference(pref)
  }

  onMount(() => {
    disableTextSubstitutions(document)

    const handleFocusIn = (event: FocusEvent) => {
      const target = event.target
      if (target instanceof HTMLElement)
        disableTextSubstitutions(target)
    }

    document.addEventListener('focusin', handleFocusIn, true)
    onCleanup(() => document.removeEventListener('focusin', handleFocusIn, true))
  })

  return (
    <div class={heightFull}>
      <Show
        when={desktopConnected()}
        fallback={<LauncherView onConnected={() => setDesktopConnected(true)} />}
      >
        <DesktopFadeIn>
          <AuthProvider>
            <PreferencesProvider>
              <PreferencesApplier>
                <Router root={props => <Suspense>{props.children}</Suspense>}>
                  <FileRoutes />
                </Router>
              </PreferencesApplier>
            </PreferencesProvider>
            <UserMenuDialogs />
          </AuthProvider>
        </DesktopFadeIn>
      </Show>
    </div>
  )
}
