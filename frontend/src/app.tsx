import type { ParentComponent } from 'solid-js'
import { Router } from '@solidjs/router'
import { FileRoutes } from '@solidjs/start/router'
import { createEffect, createSignal, ErrorBoundary, getOwner, Match, onCleanup, onMount, runWithOwner, Show, Suspense, Switch } from 'solid-js'
import { getRuntimeState, isTauriApp, platformBridge, refreshRuntimeState } from '~/api/platformBridge'
import { channelManager } from '~/api/workerRpc'
import { renderErrorFallback } from '~/components/common/ErrorFallback'
import { LauncherView } from '~/components/desktop/LauncherView'
import { AboutDialog } from '~/components/shell/AboutDialog'
import { DesktopMinimalChrome, DesktopRouteChrome } from '~/components/shell/DesktopChrome'
import { UserMenuDialogs } from '~/components/shell/UserMenu'
import { setShowAboutDialog, setShowPreferencesDialog, showAboutDialog } from '~/components/shell/UserMenuState'
import { AuthProvider } from '~/context/AuthContext'
import { PreferencesProvider, usePreferences } from '~/context/PreferencesContext'
import { useCoreShortcuts } from '~/hooks/useCoreShortcuts'
import { initStorageCleanup, KEY_BROWSER_PREFS, loadBrowserPrefs } from '~/lib/browserStorage'
import { createLogger } from '~/lib/logger'
import { disableTextSubstitutions } from '~/lib/textInputBehavior'
import { heightFull } from '~/styles/shared.css'
import '~/lib/oat'
import '@knadh/oat/oat.min.css'
import '@knadh/oat/oat.min.js'
import './styles/dropdown-flip.css'
import './styles/global.css'

const log = createLogger('app')

export type ThemePreference = 'light' | 'dark' | 'system'

/** Read the saved theme preference from localStorage (instant, no flash). */
function getStoredTheme(): ThemePreference {
  const stored = loadBrowserPrefs().theme
  if (stored === 'light' || stored === 'dark' || stored === 'system')
    return stored
  return 'system' // missing → default until account loads
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
    const setter = window.__leapmux_setTheme
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
  const [opacity, setOpacity] = createSignal(isTauriApp() ? 0 : 1)

  onMount(() => {
    if (!isTauriApp())
      return
    // Delay slightly to let auth resolve before fading in.
    const timer = setTimeout(setOpacity, 150, 1)
    onCleanup(() => clearTimeout(timer))
  })

  return (
    <div style={{ height: '100%', opacity: opacity(), transition: 'opacity var(--transition)' }}>
      {props.children}
    </div>
  )
}

export default function App() {
  const disposeStorageCleanup = initStorageCleanup()
  onCleanup(disposeStorageCleanup)

  useCoreShortcuts()

  type DesktopState = 'loading' | 'launcher' | 'connected'
  const [desktopState, setDesktopState] = createSignal<DesktopState>(isTauriApp() ? 'loading' : 'connected')
  // Expose so "Switch mode..." in the menu can reset without page reload.
  window.__leapmux_disconnectDesktop = () => {
    channelManager.closeAll()
    refreshRuntimeState()
    setDesktopState('launcher')
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

  // Follow theme changes made in other tabs.
  //
  // The event is only used to learn WHICH key changed; the value is read back
  // through `loadBrowserPrefs`. Parsing `e.newValue` directly -- which this did
  // -- reads the raw `{ v, e }` TTL envelope that `localStorageSet` writes, so
  // `JSON.parse(e.newValue).theme` was always `undefined`, the comparison
  // against an equally-undefined old value was never true, and cross-tab theme
  // sync had silently done nothing: a second tab kept the old theme until it
  // was reloaded.
  const handleStorage = (e: StorageEvent) => {
    if (e.key !== KEY_BROWSER_PREFS)
      return
    setThemePreference(getStoredTheme())
  }
  window.addEventListener('storage', handleStorage)
  onCleanup(() => window.removeEventListener('storage', handleStorage))

  // Expose setter for PreferencesContext/PreferencesApplier to update app theme.
  // Does NOT write to localStorage — callers handle storage themselves.
  window.__leapmux_setTheme = (pref: ThemePreference) => {
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

    if (isTauriApp()) {
      // Track disposal so listener subscriptions that resolve *after* unmount
      // run `unlisten` eagerly — otherwise the owner is already disposed,
      // `onCleanup` becomes a silent no-op, and the native listener leaks.
      let disposed = false
      onCleanup(() => {
        disposed = true
      })
      const owner = getOwner()
      const registerListener = (event: string, handler: () => void) => {
        platformBridge.onEvent(event, handler)
          .then((unlisten) => {
            if (disposed)
              unlisten()
            else
              runWithOwner(owner, () => onCleanup(unlisten))
          })
          .catch(err => log.warn(`onEvent(${event}) failed`, err))
      }
      registerListener('menu:show-about', () => setShowAboutDialog(true))
      registerListener('menu:show-preferences', () => setShowPreferencesDialog(true))

      getRuntimeState()
        .then((state) => {
          setDesktopState(state.connected ? 'connected' : 'launcher')
        })
        .catch(() => setDesktopState('launcher'))
    }
  })

  return (
    // The outermost net. Anything it catches -- the providers, the launcher,
    // the desktop chrome -- can only be retried by rebuilding all of them, so
    // its reset does re-run the auth bootstrap. The boundary inside the router
    // below exists so that a *route* fault never has to pay that price.
    <ErrorBoundary fallback={renderErrorFallback}>
      <div class={heightFull}>
        <Switch>
          <Match when={desktopState() === 'connected'}>
            <DesktopFadeIn>
              <AuthProvider>
                <PreferencesProvider>
                  <PreferencesApplier>
                    <Router root={props => (
                      <Suspense>
                        <DesktopRouteChrome>
                          {/*
                            Scoped below AuthProvider and PreferencesProvider deliberately:
                            a render fault in a route resets to a freshly-rendered route and
                            leaves the session, preferences and pooled channels alone. The
                            outer boundary would have torn all of them down and re-bootstrapped.

                            Being INSIDE the Suspense above is what forces `ErrorFallback` to
                            keep every suspending read out of its render: a suspended Suspense
                            with no `fallback` renders nothing, so a fallback that read a
                            pending resource would show a blank page instead of the error.
                          */}
                          <ErrorBoundary fallback={renderErrorFallback}>
                            {props.children}
                          </ErrorBoundary>
                        </DesktopRouteChrome>
                      </Suspense>
                    )}
                    >
                      <FileRoutes />
                    </Router>
                  </PreferencesApplier>
                  <UserMenuDialogs />
                </PreferencesProvider>
              </AuthProvider>
            </DesktopFadeIn>
          </Match>
          <Match when={desktopState() === 'launcher'}>
            <DesktopMinimalChrome>
              <LauncherView onConnected={() => setDesktopState('connected')} />
            </DesktopMinimalChrome>
          </Match>
        </Switch>
        <Show when={showAboutDialog()}>
          <AboutDialog onClose={() => setShowAboutDialog(false)} />
        </Show>
      </div>
    </ErrorBoundary>
  )
}
