import type { ParentComponent } from 'solid-js'
import { Router } from '@solidjs/router'
import { FileRoutes } from '@solidjs/start/router'
import { createEffect, createSignal, ErrorBoundary, getOwner, Match, onCleanup, onMount, runWithOwner, Show, Suspense, Switch } from 'solid-js'
import { getRuntimeState, isTauriApp, platformBridge, refreshRuntimeState } from '~/api/platformBridge'
import { channelManager } from '~/api/workerRpc'
import { BootSplash } from '~/components/common/BootSplash'
import { renderErrorFallback } from '~/components/common/ErrorFallback'
import { LauncherView } from '~/components/desktop/LauncherView'
import { AboutDialog } from '~/components/shell/AboutDialog'
import { DesktopMinimalChrome, DesktopRouteChrome } from '~/components/shell/DesktopChrome'
import { UserMenuDialogs } from '~/components/shell/UserMenu'
import { openPreferences, setShowAboutDialog, showAboutDialog } from '~/components/shell/UserMenuState'
import { AuthProvider } from '~/context/AuthContext'
import { PreferencesProvider, usePreferences } from '~/context/PreferencesContext'
import { useCoreShortcuts } from '~/hooks/useCoreShortcuts'
import { usePreferencesForIdentity } from '~/hooks/usePreferencesForIdentity'
import { initStorageCleanup } from '~/lib/browserStorage'
import { createLogger } from '~/lib/logger'
import { resolveSyntaxPair, resolveSyntaxVariant, setSyntaxTheme } from '~/lib/syntaxThemeStore'
import { disableTextSubstitutions } from '~/lib/textInputBehavior'
// Importing this module is what starts the live theme: it builds its store in a
// root of its own, which paints <html> with the default palette at the OS
// polarity before any component renders, and owns the prefers-color-scheme
// subscription that keeps that answer live.
import { applyTheme, themeStore } from '~/lib/themeStore'
import { heightFull } from '~/styles/shared.css'
import '~/lib/oat'
import '@knadh/oat/oat.min.css'
import '@knadh/oat/oat.min.js'
import './styles/dropdown-flip.css'
import './styles/global.css'

const log = createLogger('app')

/**
 * Syncs the resolved theme and font preferences from PreferencesContext
 * to the DOM.
 *
 * It is also the ONE component that sits inside both AuthProvider and
 * PreferencesProvider, so it hosts the work that needs the identity: seeding
 * the account-scoped device tier, and reloading the account settings after an
 * identity change. That trigger cannot live in PreferencesProvider itself:
 * `useAuth` throws without an AuthProvider, and the component tests that render
 * PreferencesProvider alone supply none.
 */
const PreferencesApplier: ParentComponent = (props) => {
  const preferences = usePreferences()

  usePreferencesForIdentity()

  // Push the resolved theme into the store that owns the live palette, so a
  // value arriving from either tier repaints. Until this runs, ~/lib/themeStore
  // shows the default palette at the OS polarity: it reads no storage, because
  // every stored theme is scoped to an account and this component is the first
  // point at which one is known.
  createEffect(() => {
    applyTheme(preferences.theme())
  })

  // The syntax theme, which cannot be applied in CSS: Shiki bakes the colours
  // into each token, so a change loads the new TextMate themes, drops the caches
  // that hold tokenized output, and re-renders. `shikiHighlighter` is the
  // SYNCHRONOUS highlighter the store must register on before it switches --
  // synchronous call sites cannot await a theme load.
  //
  // `data-code-variant` moves WITH the tokens, in this one effect. It used to be
  // written from an effect of its own, synchronously, so the surface repainted
  // to the new polarity while every already-rendered block still carried the old
  // theme's tokens -- dark on dark for the length of a chunk import, which is
  // the exact contrast failure the `--code-*` publication exists to prevent. The
  // repaint is not delayed in the case that has to stay fast: a UI polarity flip
  // leaves the pair unchanged, so `setSyntaxTheme` early-returns and the write
  // lands in a microtask, before paint. A failed import now leaves surface and
  // tokens both on the previous theme, which is consistent, and it is logged.
  //
  // `syntaxAttrGen` drops stale attribute writes when a later effect run wins
  // the race after the lazy `renderMarkdown` import — without it an older
  // `variant` can land on `<html>` after a newer one.
  let syntaxAttrGen = 0
  createEffect(() => {
    const pair = resolveSyntaxPair(
      preferences.syntaxTheme(),
      preferences.theme(),
      themeStore.systemMode(),
    )
    // Read synchronously, so `resolvedMode()` stays a tracked dependency of this
    // effect rather than being read inside the `.then`, where it would not be.
    const variant = resolveSyntaxVariant(
      preferences.syntaxTheme(),
      preferences.theme(),
      themeStore.systemMode(),
      themeStore.resolvedMode(),
    )
    const gen = ++syntaxAttrGen
    // `setSyntaxTheme` now REJECTS when a theme chunk fails to load, so the
    // rejection is reported rather than dropped. Without a handler it reaches
    // the global sink as "Something went wrong", which says nothing about the
    // code surface that did not repaint.
    //
    // `shikiHighlighter` is loaded LAZILY: a static import of `~/lib/renderMarkdown`
    // pulled the whole sync Shiki grammar set + worker client onto every cold
    // boot's modulepreload list (~2 MB of critical-path JS on mobile). The
    // highlighter is only needed once preferences resolve a syntax pair.
    void import('~/lib/renderMarkdown')
      .then(({ shikiHighlighter }) => setSyntaxTheme(pair, shikiHighlighter))
      .then(() => {
        if (gen !== syntaxAttrGen)
          return
        if (typeof document !== 'undefined') {
          document.documentElement.setAttribute('data-code-variant', variant.id)
          // The POLARITY beside the variant, because CSS cannot select on the
          // value of a custom property and `--code-color-scheme` is one. It
          // decides whether a code block's field is a translucent tint (the two
          // polarities agree, so the field may belong to whatever hosts it) or
          // an opaque mix (they differ, and the field has to carry the baked
          // tokens across the flip). Written HERE, in the same statement as the
          // variant, so the two cannot describe different themes for a frame.
          document.documentElement.setAttribute('data-code-polarity', variant.polarity)
        }
      })
      .catch((err: unknown) => {
        if (gen !== syntaxAttrGen)
          return
        console.error('[app] the syntax theme failed to load; the code surface keeps the previous one:', err)
      })
  })

  return (
    <div style={{ 'height': '100%', 'font-family': preferences.uiFontFamily() }}>
      {props.children}
    </div>
  )
}

/**
 * Wraps app content in desktop mode to prevent a brief flash of
 * BootSplash from AuthGuard while auth is resolving. Starts at
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
      registerListener('menu:show-preferences', () => openPreferences('appearance'))

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
                      <Suspense fallback={<BootSplash />}>
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
