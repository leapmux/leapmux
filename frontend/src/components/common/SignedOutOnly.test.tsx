import type { Navigator } from '@solidjs/router'
import { createMemoryHistory, MemoryRouter, Route } from '@solidjs/router'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { SignedOutOnly } from '~/components/common/SignedOutOnly'
import { resetSystemInfoMock, setSystemInfoMock } from '~/test-support/systemInfoMock'

vi.mock('~/lib/systemInfo', async () => {
  const m = await import('~/test-support/systemInfoMock')
  return m.systemInfoMock
})

// A REAL signal behind the mock, because two cases turn on reactivity: the
// gate must answer a bootstrap that resolves, and must NOT answer a sign-in
// performed on the page. A plain vi.fn cannot express either -- changing its
// return value re-renders nothing.
const [userSignal, setUserSignal] = createSignal<{ id: string, username?: string } | null>(null)
const [loadingSignal, setLoadingSignal] = createSignal(false)
const mockLogout = vi.fn(async () => {
  setUserSignal(null)
})
/* eslint-disable solid/reactivity -- these ARE the reactive reads the mocked
   context hands the component; the component under test is what tracks them,
   and the rule cannot see across the vi.mock factory below. */
const mockUser = Object.assign(() => userSignal(), {
  mockReturnValue: (v: { id: string, username?: string } | null) => setUserSignal(v),
})
const mockLoading = Object.assign(() => loadingSignal(), {
  mockReturnValue: (v: boolean) => setLoadingSignal(v),
})
/* eslint-enable solid/reactivity */

vi.mock('~/context/AuthContext', () => ({
  useAuth: () => ({
    user: mockUser,
    loading: mockLoading,
    isAuthenticated: () => mockUser() !== null,
    error: () => null,
    bootstrapError: () => null,
    retryBootstrap: async () => {},
    login: async () => {},
    logout: mockLogout,
    setAuth: () => {},
  }),
}))

const assign = vi.fn<(target: string) => void>()
vi.mock('~/lib/postAuthNavigate', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/lib/postAuthNavigate')>()
  return {
    ...actual,
    // The full-document branch cannot run in jsdom, so this mock replaces the
    // module's own assign. Everything else — safeRedirect, the server-route
    // test — stays real, because those are the parts a wrong target reaches.
    postAuthNavigate: (navigate: Navigator, target: string | undefined, fallback: string) => {
      if (target && actual.isServerRoute(target)) {
        assign(target)
        return
      }
      actual.postAuthNavigate(navigate, target, fallback)
    },
  }
})

/**
 * Renders the gate inside a REAL router, so a redirect is observable as a
 * navigation rather than as an internal call nobody can see.
 */
function renderGate(path = '/login', whenSignedIn?: 'redirect' | 'explain') {
  const history = createMemoryHistory()
  if (path !== '/')
    history.set({ value: path, replace: true, scroll: false })
  const navigations: string[] = []
  history.listen(value => navigations.push(value))

  const Restricted = () => (
    <SignedOutOnly whenSignedIn={whenSignedIn}>
      <div data-testid="credential-form" />
    </SignedOutOnly>
  )

  const result = render(() => (
    <MemoryRouter history={history}>
      <Route path="/login" component={Restricted} />
      <Route path="/signup" component={Restricted} />
      <Route path="/recover-account" component={Restricted} />
      <Route path="/recover-account/complete" component={Restricted} />
      <Route path="/" component={() => <div data-testid="app-stub" />} />
      {/* A REAL in-app address, because `postAuthNavigate` decides the branch
          from the SPA's own route table -- an invented path takes the
          full-document branch, exactly as it would in the browser. */}
      <Route path="/verify-email" component={() => <div data-testid="verify-stub" />} />
    </MemoryRouter>
  ))
  return { ...result, navigations }
}

describe('signedOutOnly', () => {
  beforeEach(() => {
    mockUser.mockReturnValue(null)
    mockLoading.mockReturnValue(false)
    resetSystemInfoMock()
    assign.mockClear()
    mockLogout.mockClear()
  })

  // THE race the latch exists to remove. Every page under this gate signs
  // somebody in and then navigates for itself, so reacting to `auth.user()`
  // becoming set put a second navigator on that one state change. Solid
  // flushes this effect before the page's own call, so the page usually wins
  // by navigating last -- but a `?redirect=` that points at a hub route takes
  // postAuthNavigate's full-document branch, and a document that already
  // started to leave cannot be called back: a CLI sign-in on a hub that
  // requires email verification left for /oauth/authorize with the emailed code
  // never shown.
  it('ignores a sign-in performed ON the page', async () => {
    mockUser.mockReturnValue(null)
    const { navigations } = renderGate('/login?redirect=%2Foauth%2Fauthorize')
    expect(screen.getByTestId('credential-form')).toBeInTheDocument()

    // The page's own sign-in lands. The gate must not answer it.
    mockUser.mockReturnValue({ id: 'u-1' })
    await vi.waitFor(() => {
      expect(screen.queryByTestId('credential-form')).not.toBeInTheDocument()
    })
    expect(assign).not.toHaveBeenCalled()
    expect(navigations).toEqual([])
  })

  // And the arrival it DOES answer still works, on the same address.
  it('sends a visitor who arrived signed in to the hub route', async () => {
    mockUser.mockReturnValue({ id: 'u-1' })
    renderGate('/login?redirect=%2Foauth%2Fauthorize')
    await vi.waitFor(() => {
      expect(assign).toHaveBeenCalledWith('/oauth/authorize')
    })
  })

  // ONCE. The effect reads the latch and then writes it, and a user effect
  // runs inside `runUpdates`, so the self-write re-queues the effect rather
  // than being dropped. The second run then found the latch already set,
  // skipped the write, and fell straight through to the navigation. On a hub
  // target that is two full-document loads of a single-use consent address.
  it('issues the post-authentication navigation once', async () => {
    mockUser.mockReturnValue({ id: 'u-1' })
    renderGate('/login?redirect=%2Foauth%2Fauthorize')
    await vi.waitFor(() => {
      expect(assign).toHaveBeenCalledWith('/oauth/authorize')
    })
    expect(assign).toHaveBeenCalledTimes(1)
  })

  // /recover-account/complete carries a SINGLE-USE token and no ?redirect=, so the
  // default silent bounce spent nothing and explained nothing: the user
  // landed on the dashboard with no idea why their emailed link went there,
  // and `replace` took the tokened address out of that tab's history too.
  it('explains rather than redirects when the page says so', async () => {
    mockUser.mockReturnValue({ id: 'u-1', username: 'alice' })
    const { navigations } = renderGate('/recover-account/complete?token=abc', 'explain')

    expect(await screen.findByTestId('signed-out-only-explain')).toBeInTheDocument()
    expect(screen.queryByTestId('credential-form')).not.toBeInTheDocument()
    expect(navigations).toEqual([])
    expect(assign).not.toHaveBeenCalled()
  })

  // The one control that helps: signing out re-renders the form at the SAME
  // address, with the token still in it and still unspent.
  it('renders the form again after signing out in place', async () => {
    mockUser.mockReturnValue({ id: 'u-1', username: 'alice' })
    renderGate('/recover-account/complete?token=abc', 'explain')

    fireEvent.click(await screen.findByTestId('signed-out-only-sign-out'))
    await vi.waitFor(() => {
      expect(screen.getByTestId('credential-form')).toBeInTheDocument()
    })
    expect(mockLogout).toHaveBeenCalledTimes(1)
  })

  it('renders the credential form for a visitor who is not signed in', () => {
    const { navigations } = renderGate()
    expect(screen.getByTestId('credential-form')).toBeInTheDocument()
    expect(navigations).toEqual([])
  })

  // The common visitor is signed out, so the form must not wait behind the
  // bootstrap. A splash here would delay every sign-in to save a redirect
  // nobody but an already-signed-in user needs.
  it('renders the form while the auth bootstrap is still running', () => {
    mockLoading.mockReturnValue(true)
    const { navigations } = renderGate()
    expect(screen.getByTestId('credential-form')).toBeInTheDocument()
    expect(navigations).toEqual([])
  })

  it('sends a signed-in visitor to the app', async () => {
    mockUser.mockReturnValue({ id: 'u-1' })
    const { navigations } = renderGate()
    await vi.waitFor(() => {
      expect(navigations).toEqual(['/'])
    })
  })

  // Not decoration: /signup restricted access on the hub's signup setting
  // alone, so a signed-in user could create a second account and the page then
  // swapped their session to it without a word.
  it('restricts every credential page, not only login', async () => {
    mockUser.mockReturnValue({ id: 'u-1' })
    const { navigations } = renderGate('/signup')
    await vi.waitFor(() => {
      expect(navigations).toEqual(['/'])
    })
  })

  // The form must be GONE in the same frame the redirect starts, or a user
  // can still type a password or spend a reset token into a page that the app
  // is about to remove.
  it('renders nothing for a signed-in visitor', () => {
    mockUser.mockReturnValue({ id: 'u-1' })
    renderGate()
    expect(screen.queryByTestId('credential-form')).not.toBeInTheDocument()
  })

  it('honors an in-app redirect target', async () => {
    mockUser.mockReturnValue({ id: 'u-1' })
    const { navigations } = renderGate('/login?redirect=%2Fverify-email')
    await vi.waitFor(() => {
      expect(navigations).toEqual(['/verify-email'])
    })
  })

  // A CLI sign-in bounces the browser to /login?redirect=/oauth/authorize...
  // Sending an already-signed-in user to `/` would end that flow with
  // nothing on screen and the CLI waiting for a consent screen.
  it('hands a hub-served redirect target back to the server', async () => {
    mockUser.mockReturnValue({ id: 'u-1' })
    renderGate('/login?redirect=%2Foauth%2Fauthorize%3Fadmin%3D1')
    await vi.waitFor(() => {
      expect(assign).toHaveBeenCalledWith('/oauth/authorize?admin=1')
    })
  })

  // A SOLO hub answers every request as the synthetic solo user, so no
  // credential page can succeed on it. Each page used to spell the rule out,
  // and it reached two of the five: /recover-account, /recover-account/complete and
  // /setup each served a form the hub has no endpoint for.
  it('sends a solo-hub visitor to the app, from every credential page', async () => {
    setSystemInfoMock({ soloMode: true })
    mockUser.mockReturnValue({ id: 'solo' })
    const { navigations } = renderGate('/recover-account')
    await vi.waitFor(() => {
      expect(navigations).toEqual(['/'])
    })
  })

  // The decision waits for the bootstrap, and that gate is the whole point:
  // before the first system-info load `isSoloMode()` answers a fabricated
  // `false`. Sampling it early gives the WRONG answer rather than an early
  // one, and an onMount that sampled it never looked again.
  it('waits for the bootstrap before it answers a solo hub', async () => {
    mockLoading.mockReturnValue(true)
    const { navigations } = renderGate('/login')
    expect(screen.getByTestId('credential-form')).toBeInTheDocument()
    expect(navigations).toEqual([])

    setSystemInfoMock({ soloMode: true })
    mockUser.mockReturnValue({ id: 'solo' })
    mockLoading.mockReturnValue(false)

    await vi.waitFor(() => {
      expect(navigations).toEqual(['/'])
    })
  })

  // The solo rule OUTRANKS `whenSignedIn`. Offering to sign out is the wrong
  // answer on a hub where signing out is impossible, and there is no password
  // to reset either.
  it('redirects rather than explains on a solo hub', async () => {
    setSystemInfoMock({ soloMode: true })
    mockUser.mockReturnValue({ id: 'solo', username: 'solo' })
    const { navigations } = renderGate('/recover-account/complete?token=abc', 'explain')

    await vi.waitFor(() => {
      expect(navigations).toEqual(['/'])
    })
    expect(screen.queryByTestId('signed-out-only-explain')).not.toBeInTheDocument()
  })

  // safeRedirect is the guard, and it lives in postAuthNavigate rather than
  // here. This proves the gate routes through it instead of navigating to
  // whatever the query says.
  it('drops an off-origin redirect target and uses the app', async () => {
    mockUser.mockReturnValue({ id: 'u-1' })
    const { navigations } = renderGate('/login?redirect=https%3A%2F%2Fevil.test%2F')
    await vi.waitFor(() => {
      expect(navigations).toEqual(['/'])
    })
    expect(assign).not.toHaveBeenCalled()
  })
})
