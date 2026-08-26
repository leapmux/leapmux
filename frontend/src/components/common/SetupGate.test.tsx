import type { JSX } from 'solid-js'
import { createMemoryHistory, MemoryRouter, Route, useNavigate } from '@solidjs/router'
import { render, screen } from '@solidjs/testing-library'
import { createEffect, createSignal } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { SetupGate } from '~/components/common/SetupGate'
import { resetSystemInfoMock, setSystemInfoMock } from '~/test-support/systemInfoMock'

vi.mock('~/lib/systemInfo', async () => {
  const m = await import('~/test-support/systemInfoMock')
  return m.systemInfoMock
})

// REAL signals behind the auth mock: every case here turns on the bootstrap
// resolving, which a plain vi.fn cannot express -- changing its return value
// re-renders nothing.
const [authLoading, setAuthLoading] = createSignal(false)
const [authUser, setAuthUser] = createSignal<{ id: string } | null>(null)

vi.mock('~/context/AuthContext', () => ({
  useAuth: () => ({
    user: () => authUser(),
    loading: () => authLoading(),
    isAuthenticated: () => authUser() !== null,
    error: () => null,
    bootstrapError: () => null,
    retryBootstrap: async () => {},
    login: async () => {},
    logout: async () => {},
    setAuth: () => {},
  }),
}))

/** Every address the SPA router serves, plus one it does not. */
const ROUTES = [
  '/',
  '/login',
  '/signup',
  '/forgot-password',
  '/reset-password',
  '/verify-email',
  '/elevate',
  '/oauth/complete-signup',
  '/no-such-page',
]

/**
 * Renders the gate around a stub route tree, in a REAL router so a redirect
 * is observable as a navigation rather than as an internal call nobody can
 * see.
 *
 * `child` stands in for whatever the outlet would render. The default is
 * inert; the fixed-point case passes one that navigates for itself, which is
 * what AuthGuard and LoginPage do below this gate in production.
 */
function renderGate(path: string, child: () => JSX.Element = () => <div data-testid="route-content" />) {
  const history = createMemoryHistory()
  if (path !== '/')
    history.set({ value: path, replace: true, scroll: false })

  // Listen only after the initial entry lands, so `navigations` holds exactly
  // the redirects the gate caused.
  const navigations: string[] = []
  history.listen(value => navigations.push(value))

  const Gated = () => <SetupGate>{child()}</SetupGate>

  const result = render(() => (
    <MemoryRouter history={history}>
      {/* One definition for every address, which `Route` accepts as an array. */}
      <Route path={ROUTES} component={Gated} />
      {/*
        `/setup` alone, exactly as the file route declares it. The matcher
        trims a trailing slash, so this also serves `/setup/` -- which is the
        disagreement the gate has to normalize away.
      */}
      <Route path="/setup" component={() => <SetupGate><div data-testid="setup-page" /></SetupGate>} />
    </MemoryRouter>
  ))
  return { ...result, history, navigations, currentPath: () => history.get() }
}

describe('setupGate', () => {
  beforeEach(() => {
    resetSystemInfoMock()
    setAuthLoading(false)
    setAuthUser(null)
  })

  describe('when the hub has no account yet', () => {
    beforeEach(() => {
      setSystemInfoMock({ setupRequired: true })
    })

    // The whole point: /setup is the only page that can do anything on a hub
    // with no account, so every other address must lead there -- including the
    // ones no page ever carried a check of its own for.
    for (const path of ROUTES) {
      it(`sends ${path} to /setup`, async () => {
        renderGate(path)
        expect(await screen.findByTestId('setup-page')).toBeInTheDocument()
        expect(screen.queryByTestId('route-content')).not.toBeInTheDocument()
      })
    }

    it('renders /setup itself', () => {
      const { navigations } = renderGate('/setup')
      expect(screen.getByTestId('setup-page')).toBeInTheDocument()
      expect(navigations).toEqual([])
    })

    it('renders /setup with a trailing slash without a bounce', () => {
      const { navigations } = renderGate('/setup/')
      expect(screen.getByTestId('setup-page')).toBeInTheDocument()
      expect(navigations).toEqual([])
    })

    it('replaces rather than pushes, so Back does not return to the dead end', async () => {
      const history = createMemoryHistory()
      history.set({ value: '/login', replace: true, scroll: false })
      render(() => (
        <MemoryRouter history={history}>
          <Route path="/login" component={() => <SetupGate><div data-testid="route-content" /></SetupGate>} />
          <Route path="/setup" component={() => <div data-testid="setup-page" />} />
        </MemoryRouter>
      ))
      await screen.findByTestId('setup-page')
      history.back()
      // A pushed entry would land back on /login here.
      expect(history.get()).not.toBe('/login')
    })

    // The redirect is decided but the navigation has not landed, so the form
    // must already be gone: a page on its way out can still take a password.
    it('hides the route while the redirect is in flight', () => {
      renderGate('/login')
      expect(screen.queryByTestId('route-content')).not.toBeInTheDocument()
      expect(screen.getByTestId('boot-splash')).toBeInTheDocument()
    })

    // A session PROVES an account exists, so a snapshot that still says
    // `setupRequired` is stale. This is what carries the new administrator
    // home: /setup adopts the session and navigates to `/` while its forced
    // re-fetch of the system info is still in flight.
    it('lets a signed-in visitor through on a stale snapshot', () => {
      setAuthUser({ id: 'u-1' })
      const { navigations } = renderGate('/')
      expect(screen.getByTestId('route-content')).toBeInTheDocument()
      expect(navigations).toEqual([])
    })

    // THE ordering hazard. When the bootstrap resolves, the gate and the page
    // below it read the same change, and every page below carries a
    // navigation of its own: AuthGuard sends an unauthenticated visitor to
    // `/login`, LoginPage sends a solo one to `/`.
    //
    // The page never gets to act on it. `Show` is a render effect, so it
    // tears the subtree down in the update phase, and Solid skips a queued
    // user effect whose owner is already disposed. The competing navigation
    // is not ordered after the gate's -- it does not happen.
    it('tears the route down before the route can act on the same change', async () => {
      setAuthLoading(true)
      const competed = vi.fn()
      const NavigatingChild = () => {
        const navigate = useNavigate()
        createEffect(() => {
          if (authLoading())
            return
          competed()
          navigate('/login?redirect=%2F', { replace: true })
        })
        return <div data-testid="route-content" />
      }

      const { currentPath } = renderGate('/', () => <NavigatingChild />)
      expect(screen.getByTestId('route-content')).toBeInTheDocument()

      setAuthLoading(false)

      expect(await screen.findByTestId('setup-page')).toBeInTheDocument()
      expect(currentPath()).toBe('/setup')
      expect(competed).not.toHaveBeenCalled()
    })

    // And the second layer, for a navigation the teardown above cannot
    // cancel: one that starts later, from a timer, a socket callback, or the
    // user's own address bar. The gate refuses every address but /setup while
    // an account is missing, so the state is a fixed point -- whatever reaches
    // the router is answered by the same redirect again.
    it('answers a later navigation to any other address', async () => {
      const { history } = renderGate('/setup')
      expect(screen.getByTestId('setup-page')).toBeInTheDocument()

      history.set({ value: '/login?redirect=%2F' })

      await vi.waitFor(() => {
        expect(history.get()).toBe('/setup')
      })
      expect(screen.getByTestId('setup-page')).toBeInTheDocument()
    })
  })

  describe('when the hub is already set up', () => {
    it('sends /setup to /login', async () => {
      const { navigations } = renderGate('/setup')
      await screen.findByTestId('route-content')
      expect(navigations).toEqual(['/login'])
    })

    // `<Route path="/setup">` matches this, so without the normalization the
    // gate compared `/setup/` against `/setup`, decided the visitor was
    // somewhere else, and served the first-run form on a hub that already had
    // an administrator.
    it('sends /setup with a trailing slash to /login as well', async () => {
      const { navigations } = renderGate('/setup/')
      await screen.findByTestId('route-content')
      expect(navigations).toEqual(['/login'])
    })

    it('leaves every other address alone', () => {
      for (const path of ROUTES) {
        const { navigations, unmount } = renderGate(path)
        expect(screen.getByTestId('route-content'), path).toBeInTheDocument()
        expect(navigations, path).toEqual([])
        unmount()
      }
    })
  })

  // The getters are fabrications until the first system-info load lands, so
  // deciding early is deciding on a guess -- and the guess (`setupRequired =
  // false`) is the wrong one for exactly the hub this gate exists to serve.
  describe('before the answers arrive', () => {
    it('decides nothing while the bootstrap is in flight, then redirects', async () => {
      setAuthLoading(true)
      setSystemInfoMock({ setupRequired: true })

      const { navigations } = renderGate('/login')
      // The common visitor is signed out on a hub that is set up; holding the
      // form behind a splash would delay every sign-in to answer a question
      // that concerns one installation once.
      expect(screen.getByTestId('route-content')).toBeInTheDocument()
      expect(navigations).toEqual([])

      setAuthLoading(false)
      expect(await screen.findByTestId('setup-page')).toBeInTheDocument()
    })

    // The snapshot can change AFTER the gate has already decided: a forced
    // re-fetch lands when an admin toggles a setting, and setup itself flips
    // this flag. A memo that sampled the getter once would strand a visitor on
    // the page the previous answer chose.
    it('follows a setup state that changes after it has decided', async () => {
      setSystemInfoMock({ setupRequired: true })
      const { navigations } = renderGate('/setup')
      expect(screen.getByTestId('setup-page')).toBeInTheDocument()
      expect(navigations).toEqual([])

      setSystemInfoMock({ setupRequired: false })

      await vi.waitFor(() => {
        expect(navigations).toEqual(['/login'])
      })
    })

    // The hub is unreachable, so `setupRequired` never got a real value.
    // Bouncing the operator off /setup here would take away the one page they
    // need and offer a login form that cannot work either; AuthGuard renders
    // the real diagnosis for `/`.
    it('decides nothing when the system info never loaded', () => {
      setSystemInfoMock({ loaded: false })
      const { navigations } = renderGate('/setup')
      expect(screen.getByTestId('setup-page')).toBeInTheDocument()
      expect(navigations).toEqual([])
    })
  })
})
