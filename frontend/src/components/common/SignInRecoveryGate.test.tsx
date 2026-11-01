import type { SignInRecovery } from '~/context/AuthContext'
import { createMemoryHistory, MemoryRouter, Route } from '@solidjs/router'
/// <reference types="vitest/globals" />
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { SignInRecoveryGate } from './SignInRecoveryGate'

// The recovery state lives in a real signal, so the gate re-renders exactly
// as it does over the real context.
const [recovery, setRecovery] = createSignal<SignInRecovery | null>(null)
const mockRetry = vi.fn(async () => {})
const mockFinish = vi.fn(() => setRecovery(null))
vi.mock('~/context/AuthContext', () => ({
  useAuth: () => ({
    signInRecovery: () => recovery(),
    retrySignInRecovery: mockRetry,
    finishSignInRecovery: mockFinish,
  }),
}))

const assign = vi.fn()

/** How many times each route component mounted. */
const mounts = {
  login: 0,
  signup: 0,
  home: 0,
  verify: 0,
}

function renderGate(path: string) {
  const history = createMemoryHistory()
  history.set({ value: path, replace: true, scroll: false })
  const navigations: string[] = []
  history.listen(value => navigations.push(value))
  render(() => (
    <MemoryRouter history={history} root={props => <SignInRecoveryGate>{props.children}</SignInRecoveryGate>}>
      <Route
        path="/login"
        component={() => {
          mounts.login++
          return <div data-testid="login-page" />
        }}
      />
      <Route
        path="/signup"
        component={() => {
          mounts.signup++
          return <div data-testid="signup-page" />
        }}
      />
      <Route
        path="/verify-email"
        component={() => {
          mounts.verify++
          return <div data-testid="verify-email-page" />
        }}
      />
      <Route
        path="/"
        component={() => {
          mounts.home++
          return <div data-testid="app-home" />
        }}
      />
    </MemoryRouter>
  ))
  return { history, navigations }
}

/** Recovery in a given phase; `redirect` is the address the sign-in asked for. */
function recoveryState(fields: Partial<SignInRecovery>): SignInRecovery {
  return {
    error: null,
    verificationRequired: false,
    completed: false,
    ...fields,
  }
}

const CONSENT = '/oauth/authorize?client_id=leapmux-cli&state=s1'

describe('SignInRecoveryGate', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    setRecovery(null)
    vi.stubGlobal('location', { assign })
    mounts.login = 0
    mounts.signup = 0
    mounts.home = 0
    mounts.verify = 0
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('renders the routes while no recovery runs', async () => {
    renderGate('/login')
    expect(await screen.findByTestId('login-page')).toBeInTheDocument()
    expect(screen.queryByTestId('signin-recovery')).not.toBeInTheDocument()
  })

  // The gate is the outermost route gate, so a failed recovery replaces every
  // address alike: no route component mounts beneath it.
  for (const path of ['/login', '/verify-email', '/']) {
    it(`replaces ${path} with the recovery panel after a failed read`, async () => {
      setRecovery(recoveryState({ error: 'account read failed' }))
      renderGate(path)

      const panel = await screen.findByTestId('signin-recovery')
      expect(panel).toHaveTextContent('You are signed in, but your account could not be loaded.')
      expect(panel).toHaveTextContent('account read failed')
      expect(screen.queryByTestId('login-page')).not.toBeInTheDocument()
      expect(screen.queryByTestId('verify-email-page')).not.toBeInTheDocument()
      expect(screen.queryByTestId('app-home')).not.toBeInTheDocument()
    })
  }

  it('shows a pending screen while the read runs, and Retry re-reads after a failure', async () => {
    setRecovery(recoveryState({}))
    renderGate('/login')
    expect(await screen.findByTestId('signin-recovery-pending')).toHaveTextContent('Loading your account')

    setRecovery(recoveryState({ error: 'account read failed' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Retry' }))
    expect(mockRetry).toHaveBeenCalledOnce()
  })

  // Verification wins over the consent redirect, as on the login page, and
  // /verify-email is a client-side route: the document never leaves.
  it('reaches /verify-email from a consent redirect when verification is required', async () => {
    renderGate(`/login?redirect=${encodeURIComponent(CONSENT)}`)
    expect(await screen.findByTestId('login-page')).toBeInTheDocument()
    const loginMounts = mounts.login

    setRecovery(recoveryState({ redirect: CONSENT, verificationRequired: true, completed: true }))

    expect(await screen.findByTestId('verify-email-page')).toBeInTheDocument()
    expect(assign).not.toHaveBeenCalled()
    expect(mockFinish).toHaveBeenCalledOnce()
    expect(mounts.login).toBe(loginMounts)
  })

  // The consent page belongs to the hub's mux, so recovery hands it back to the
  // server with one full-document load, and never clears its own state: the
  // document leaves.
  it('loads the consent page with one full-document navigation', async () => {
    renderGate(`/signup?redirect=${encodeURIComponent(CONSENT)}`)
    expect(await screen.findByTestId('signup-page')).toBeInTheDocument()

    setRecovery(recoveryState({ redirect: CONSENT, completed: true }))

    await vi.waitFor(() => expect(assign).toHaveBeenCalledOnce())
    expect(assign).toHaveBeenCalledWith(CONSENT)
    expect(mockFinish).not.toHaveBeenCalled()
  })

  it('lands on / without a redirect or a verification requirement', async () => {
    renderGate('/login')
    expect(await screen.findByTestId('login-page')).toBeInTheDocument()

    setRecovery(recoveryState({ completed: true }))

    expect(await screen.findByTestId('app-home')).toBeInTheDocument()
    expect(mockFinish).toHaveBeenCalledOnce()
    expect(assign).not.toHaveBeenCalled()
  })

  // The gate clears recovery only once the router's location IS the
  // destination, so the query string has to match too.
  it('follows a client-side redirect with a query, and clears recovery on arrival', async () => {
    const { navigations } = renderGate('/login')
    expect(await screen.findByTestId('login-page')).toBeInTheDocument()

    setRecovery(recoveryState({ redirect: '/?from=cli', completed: true }))

    expect(await screen.findByTestId('app-home')).toBeInTheDocument()
    expect(navigations).toEqual(['/?from=cli'])
    expect(mockFinish).toHaveBeenCalledOnce()
    expect(assign).not.toHaveBeenCalled()
  })

  // The router matches `/a/` as `/a`, so a redirect with a trailing slash
  // reaches the route, and the gate still clears recovery there.
  it('clears recovery after a redirect with a trailing slash', async () => {
    renderGate('/login')
    expect(await screen.findByTestId('login-page')).toBeInTheDocument()

    setRecovery(recoveryState({ redirect: '/verify-email/', completed: true }))

    expect(await screen.findByTestId('verify-email-page')).toBeInTheDocument()
    await vi.waitFor(() => expect(mockFinish).toHaveBeenCalledOnce())
  })

  // A redirect that leaves the origin is dropped, as on the login page, and
  // the fallback is the app home, never a full-document load of the target.
  it('lands on / for a redirect that leaves the origin', async () => {
    const { navigations } = renderGate('/login')
    expect(await screen.findByTestId('login-page')).toBeInTheDocument()

    setRecovery(recoveryState({ redirect: 'https://evil.example/', completed: true }))

    expect(await screen.findByTestId('app-home')).toBeInTheDocument()
    expect(navigations).toEqual(['/'])
    expect(assign).not.toHaveBeenCalled()
    expect(mockFinish).toHaveBeenCalledOnce()
  })

  // An immediately successful first read is still ONE navigation, owned by the
  // gate; and the source page never remounts between adoption and arrival.
  it('navigates once and never remounts the source page', async () => {
    const { navigations } = renderGate('/login')
    expect(await screen.findByTestId('login-page')).toBeInTheDocument()
    const loginMounts = mounts.login

    setRecovery(recoveryState({}))
    expect(await screen.findByTestId('signin-recovery-pending')).toBeInTheDocument()
    setRecovery(recoveryState({ completed: true }))

    expect(await screen.findByTestId('app-home')).toBeInTheDocument()
    expect(navigations).toEqual(['/'])
    expect(mounts.home).toBe(1)
    expect(mounts.login).toBe(loginMounts)
  })
})
