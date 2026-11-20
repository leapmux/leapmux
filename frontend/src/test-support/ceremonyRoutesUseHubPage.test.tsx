import type { JSX } from 'solid-js'
import type { GetCurrentUserResponse } from '~/generated/proto/leapmux/v1/auth_pb'
import type { BrowserAuthFlow } from '~/lib/browserAuth'
import type { CeremonyConfig } from '~/routes/(ceremonies)'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { create } from '@bufbuild/protobuf'
import { Code, ConnectError } from '@connectrpc/connect'
import { createMemoryHistory, MemoryRouter, Route, useBeforeLeave, useNavigate } from '@solidjs/router'
import { cleanup, fireEvent, render, screen } from '@solidjs/testing-library'
import { onMount } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { SignInRecoveryGate } from '~/components/common/SignInRecoveryGate'
import { AuthProvider, useAuth } from '~/context/AuthContext'
import { GetCurrentUserResponseSchema } from '~/generated/proto/leapmux/v1/auth_pb'
import CeremonyLayout, { ceremonyConfigFor, ceremonyConfigs } from '~/routes/(ceremonies)'
import { deferred, settle } from '~/test-support/async'

const ceremoniesInBrowser = vi.hoisted(() => vi.fn<() => boolean>(() => false))
vi.mock('~/lib/hubSite', () => ({
  ceremoniesRunInBrowser: () => ceremoniesInBrowser(),
  hubOrigin: () => 'https://hub.example',
  hubPageUrl: (path: string) => `https://hub.example${path}`,
  openHubPage: vi.fn(async () => {}),
}))

const formMounted = vi.fn()
const runBrowserAuth = vi.hoisted(() => vi.fn<() => Promise<BrowserAuthFlow>>())
vi.mock('~/lib/browserAuth', () => ({ runBrowserAuth }))
vi.mock('~/api/transport', () => ({
  advanceAuthSessionEpoch: vi.fn(),
  loadTimeouts: vi.fn(async () => {}),
  setElevationAdoptionOpener: vi.fn(),
  setOnAuthError: vi.fn(),
}))

vi.mock('~/api/workerRpc', () => ({
  channelManager: {
    closeAll: vi.fn(),
  },
}))

vi.mock('~/lib/systemInfo', () => ({
  isAutoAuthenticated: () => false,
  isPasswordSetupGate: () => false,
  isEmailEnabled: () => false,
  isCaptchaEnabled: () => false,
  isCaptchaUnsolvableHere: () => false,
  isSystemInfoLoaded: () => true,
  loadSystemInfo: vi.fn(async () => {}),
  refreshSnapshot: vi.fn(async () => {}),
}))

const getCurrentUser = vi.hoisted(() => vi.fn<() => Promise<GetCurrentUserResponse>>())
vi.mock('~/api/clients', () => ({
  authClient: {
    getPendingOAuthSignup: vi.fn(),
    completeOAuthSignup: vi.fn(),
    login: vi.fn(),
    logout: vi.fn(),
    getCurrentUser,
  },
}))

vi.mock('~/components/common/CaptchaSection', () => ({
  CaptchaSection: (): JSX.Element => null,
}))

vi.mock('~/lib/clipboard', () => ({
  copyTextToClipboard: vi.fn(async () => true),
}))

vi.mock('~/api/platformBridge', () => ({
  desktopHub: () => ({ kind: 'remote', url: 'https://hub.example' }),
  openExternalUrl: vi.fn(async () => {}),
  platformBridge: {
    startBrowserAuth: vi.fn(),
    cancelBrowserAuth: vi.fn(),
    switchMode: vi.fn(),
    resetTunnels: vi.fn(async () => {}),
    onEvent: vi.fn(async () => vi.fn(() => {})),
  },
}))

// The route modules pull their pages in; the render tests below stub the
// child route, so the real pages only need to import without touching the
// DOM or the network.
vi.mock('~/components/common/LoginPage', () => ({
  LoginPage: () => <div data-testid="login-page" />,
}))
vi.mock('~/components/common/SignupPage', () => ({
  SignupPage: () => <div data-testid="signup-page" />,
}))
vi.mock('~/components/common/SetupPage', () => ({
  SetupPage: () => <div data-testid="setup-page" />,
}))
vi.mock('~/components/common/VerifyEmailPage', () => ({
  VerifyEmailPage: () => <div data-testid="verify-email-page" />,
}))
vi.mock('~/components/common/ElevatePage', () => ({
  ElevatePage: () => <div data-testid="elevate-page" />,
}))
vi.mock('~/components/common/RecoverPage', () => ({
  RecoverPage: () => <div data-testid="recover-page" />,
}))
vi.mock('~/components/common/RecoverCompletePage', () => ({
  RecoverCompletePage: () => <div data-testid="recover-complete-page" />,
}))

/**
 * Every route module of the `(ceremonies)` group, keyed by its file path.
 * The glob is the route set itself: a file added to the group joins it with
 * no edit here, and a test file beside a route serves no address.
 */
const routeModules = import.meta.glob(
  [
    '../routes/\\(ceremonies\\)/**/*.{ts,tsx}',
    '!../routes/\\(ceremonies\\)/**/*.test.*',
    '!../routes/\\(ceremonies\\)/**/*.spec.*',
    '!../routes/\\(ceremonies\\)/**/*.css.*',
  ],
  { eager: true },
) as Record<string, { ceremonyConfig?: CeremonyConfig }>

/** The address the router serves for one group file: `/x/index.tsx` is `/x`. */
function addressFor(modulePath: string): string {
  const segments = modulePath
    .replace(/^.*\(ceremonies\)\//, '')
    .replace(/\.(?:ts|tsx)$/, '')
    .split('/')
  if (segments.at(-1) === 'index')
    segments.pop()
  return `/${segments.join('/')}`.toLowerCase()
}

describe('ceremonyConfig', () => {
  it('is declared by every route module of the group', () => {
    expect(Object.keys(routeModules).length).toBeGreaterThan(0)
    for (const [modulePath, mod] of Object.entries(routeModules)) {
      expect(mod.ceremonyConfig, `${modulePath} must export ceremonyConfig`).toBeDefined()
      expect(['redirect', 'explain', false], modulePath).toContain(mod.ceremonyConfig!.gate)
      if (mod.ceremonyConfig!.inDesktop !== undefined)
        expect(typeof mod.ceremonyConfig!.inDesktop, modulePath).toBe('function')
    }
  })

  it('is registered under the address the router serves', () => {
    for (const [modulePath, mod] of Object.entries(routeModules))
      expect(ceremonyConfigFor(addressFor(modulePath)), modulePath).toBe(mod.ceremonyConfig)
  })

  it('has no registration without a route module', () => {
    const addresses = Object.keys(routeModules).map(addressFor).sort()
    expect([
      ...ceremonyConfigs.keys(),
    ].sort()).toEqual(addresses)
  })
})

function account(verificationRequired = false): GetCurrentUserResponse {
  return create(GetCurrentUserResponseSchema, {
    sessionReport: {
      user: { id: 'u1', username: 'alice' },
      emailVerification: { verificationRequired },
    },
  })
}

describe('CeremonyLayout', () => {
  beforeEach(() => {
    ceremoniesInBrowser.mockReturnValue(false)
    getCurrentUser.mockReset()
    getCurrentUser.mockRejectedValue(new ConnectError('no session', Code.Unauthenticated))
    runBrowserAuth.mockReset()
    formMounted.mockClear()
  })

  it('renders every ceremony route through HubPage', () => {
    const source = readFileSync(resolve(import.meta.dirname, '..', 'routes', '(ceremonies).tsx'), 'utf-8')
    expect(source).toContain('<HubPage')
  })

  it('passes the in-app sign-in slot through at /login in remote-Hub mode', async () => {
    ceremoniesInBrowser.mockReturnValue(true)
    renderCeremony('/login')

    // BrowserSignIn, not the ContinueInBrowser fallback HubPage would take
    // without the slot.
    expect(await screen.findByRole('button', { name: 'Sign in with your browser' })).toBeInTheDocument()
    expect(screen.queryByText('Continue in your browser')).toBeNull()
  })

  it('renders the default desktop fallback for a route without a slot', async () => {
    ceremoniesInBrowser.mockReturnValue(true)
    renderCeremony('/signup')

    expect(await screen.findByText('Continue in your browser')).toBeInTheDocument()
  })

  // The gate wraps the page and HubPage wraps the content, so a signed-in
  // arrival is answered by the gate even where HubPage cannot render the page.
  it('runs the explain gate behind the remote-Hub fallback', async () => {
    ceremoniesInBrowser.mockReturnValue(true)
    getCurrentUser.mockResolvedValueOnce(account())
    renderCeremony('/recover-account/complete')

    expect(await screen.findByTestId('signed-out-only-explain')).toBeInTheDocument()
    expect(screen.queryByText('Continue in your browser')).toBeNull()
  })

  it('runs the default redirect gate behind the remote-Hub fallback', async () => {
    ceremoniesInBrowser.mockReturnValue(true)
    getCurrentUser.mockResolvedValueOnce(account())
    const { navigations } = renderCeremony('/recover-account')

    await vi.waitFor(() => expect(navigations).toEqual(['/']))
    expect(screen.queryByText('Continue in your browser')).toBeNull()
  })

  it('redirects a signed-in browser visitor without mounting the credential form', async () => {
    getCurrentUser.mockResolvedValueOnce(account())
    const { auth, navigate, navigations } = renderCeremony('/')
    await vi.waitFor(() => expect(auth().user()?.username).toBe('alice'))

    navigate(`/login?redirect=${encodeURIComponent('/?signed-in')}`)

    await vi.waitFor(() => expect(navigations.at(-1)).toBe('/?signed-in'))
    expect(formMounted).not.toHaveBeenCalled()
  })

  async function reachBrowserSignInAdoption(initialPath = '/login', beforeLeave?: () => void) {
    ceremoniesInBrowser.mockReturnValue(true)
    const result = deferred<{ ok: boolean, failure: string }>()
    runBrowserAuth.mockResolvedValue({
      url: 'https://hub.example/authorize',
      result: result.promise,
      cancel: vi.fn(async () => {}),
    })
    const router = renderCeremony(initialPath, beforeLeave)
    await vi.waitFor(() => expect(router.auth().loading()).toBe(false))
    const read = deferred<GetCurrentUserResponse>()
    getCurrentUser.mockReturnValueOnce(read.promise)
    fireEvent.click(await screen.findByRole('button', { name: 'Sign in with your browser' }))
    result.resolve({ ok: true, failure: '' })
    await vi.waitFor(() => expect(getCurrentUser).toHaveBeenCalledTimes(2))
    return { ...router, read }
  }

  it('lets an adopted sign-in navigate after its own identity hides the form', async () => {
    const formWasHiddenBeforeNavigation: boolean[] = []
    const { auth, navigations, read } = await reachBrowserSignInAdoption('/login', () => {
      formWasHiddenBeforeNavigation.push(screen.queryByRole('heading', { name: 'Sign in to LeapMux' }) === null)
    })
    expect(screen.getByRole('heading', { name: 'Sign in to LeapMux' })).toBeInTheDocument()

    read.resolve(account(true))

    await vi.waitFor(() => expect(navigations).toEqual(['/verify-email']))
    expect(formWasHiddenBeforeNavigation).toEqual([true])
    expect(auth().user()?.username).toBe('alice')
    expect(await screen.findByText('Continue in your browser')).toBeInTheDocument()
  })

  it('does not let adoption navigate after the visitor leaves the login address', async () => {
    const { auth, navigate, navigations, read } = await reachBrowserSignInAdoption()
    navigate('/?staying')
    await vi.waitFor(() => expect(navigations).toEqual(['/?staying']))

    read.resolve(account(true))
    await vi.waitFor(() => expect(auth().user()?.username).toBe('alice'))
    await settle()

    expect(navigations).toEqual(['/?staying'])
    expect(screen.getByTestId('home')).toBeInTheDocument()
  })

  it('does not let adoption navigate after the visitor leaves and returns to the login address', async () => {
    const { auth, navigate, navigations, read } = await reachBrowserSignInAdoption()
    navigate('/?away')
    await vi.waitFor(() => expect(navigations).toEqual(['/?away']))
    const returnedLogin = `/login?redirect=${encodeURIComponent('/?new-arrival')}`
    navigate(returnedLogin)
    await vi.waitFor(() => expect(navigations).toEqual(['/?away', returnedLogin]))

    read.resolve(account(true))

    await vi.waitFor(() => expect(navigations).toEqual(['/?away', returnedLogin, '/?new-arrival']))
    expect(auth().user()?.username).toBe('alice')
    expect(screen.getByTestId('home')).toBeInTheDocument()
  })

  const originalLogin = `/login?redirect=${encodeURIComponent('/?abandoned')}`
  const nextLogin = `/login?redirect=${encodeURIComponent('/?current#destination')}`
  const retryLogin = `/login?redirect=${encodeURIComponent('/?retried#destination')}`
  it.each([
    {
      change: 'replaces the redirect',
      path: nextLogin,
      destination: '/?current#destination',
      verificationRequired: false,
    },
    {
      change: 'removes the redirect',
      path: '/login',
      destination: '/',
      verificationRequired: false,
    },
    {
      change: 'changes only the fragment',
      path: `${originalLogin}#changed`,
      destination: '/?abandoned',
      verificationRequired: false,
    },
    {
      change: 'replaces the redirect before email verification',
      path: nextLogin,
      destination: `/verify-email?redirect=${encodeURIComponent('/?current#destination')}`,
      verificationRequired: true,
    },
  ])('finishes adoption when the visitor $change', async ({ path, destination, verificationRequired }) => {
    const { auth, navigate, navigations, read } = await reachBrowserSignInAdoption(originalLogin)
    navigate(path)
    await vi.waitFor(() => expect(navigations).toEqual([path]))

    read.resolve(account(verificationRequired))

    await vi.waitFor(() => expect(navigations).toEqual([path, destination]))
    expect(auth().user()?.username).toBe('alice')
    expect(screen.queryByRole('heading', { name: 'Sign in to LeapMux' })).toBeNull()
  })

  it.each([
    { destination: '/?retried#destination', verificationRequired: false },
    {
      destination: `/verify-email?redirect=${encodeURIComponent('/?retried#destination')}`,
      verificationRequired: true,
    },
  ])('recovers through $destination when the initial account read fails', async ({ destination, verificationRequired }) => {
    const { auth, navigate, navigations, read } = await reachBrowserSignInAdoption(originalLogin)
    navigate(nextLogin)
    await vi.waitFor(() => expect(navigations).toEqual([nextLogin]))
    getCurrentUser.mockRejectedValueOnce(new ConnectError('temporary outage', Code.Unavailable))

    read.reject(new ConnectError('initial read failed', Code.Unavailable))
    expect(await screen.findByTestId('signin-recovery')).toHaveTextContent('temporary outage')
    expect(screen.queryByRole('heading', { name: 'Sign in to LeapMux' })).toBeNull()
    expect(navigations).toEqual([nextLogin])

    const retry = deferred<GetCurrentUserResponse>()
    getCurrentUser.mockReturnValueOnce(retry.promise)
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(await screen.findByTestId('signin-recovery-pending')).toBeInTheDocument()
    navigate(retryLogin)
    await vi.waitFor(() => expect(navigations).toEqual([nextLogin, retryLogin]))
    retry.resolve(account(verificationRequired))

    await vi.waitFor(() => expect(navigations).toEqual([nextLogin, retryLogin, destination]))
    await vi.waitFor(() => expect(auth().signInRecovery()).toBeNull())
    expect(auth().user()?.username).toBe('alice')
    expect(getCurrentUser).toHaveBeenCalledTimes(4)
    expect(runBrowserAuth).toHaveBeenCalledOnce()
  })

  it('restores the route the visitor chose before desktop sign-in recovery began', async () => {
    const { auth, navigate, navigations, read } = await reachBrowserSignInAdoption(originalLogin)
    const current = '/?staying#place'
    navigate(current)
    await vi.waitFor(() => expect(navigations).toEqual([current]))
    const recoveryRead = deferred<GetCurrentUserResponse>()
    getCurrentUser.mockReturnValueOnce(recoveryRead.promise)

    read.reject(new ConnectError('initial read failed', Code.Unavailable))
    expect(await screen.findByTestId('signin-recovery-pending')).toBeInTheDocument()
    recoveryRead.resolve(account())

    expect(await screen.findByTestId('home')).toBeInTheDocument()
    expect(auth().signInRecovery()).toBeNull()
    expect(auth().user()?.username).toBe('alice')
    expect(navigations).toEqual([current])
  })

  it('renders the routes without a gate for a signed-in visitor', async () => {
    for (const address of ['/verify-email', '/elevate', '/auth/idp/complete-signup']) {
      getCurrentUser.mockResolvedValueOnce(account())
      const { auth, navigations } = renderCeremony(address)
      await vi.waitFor(() => expect(auth().user()?.username).toBe('alice'))
      expect(screen.getByTestId('ceremony-stub'), address).toBeInTheDocument()
      expect(navigations, address).toEqual([])
      cleanup()
    }
  })
})

function Stub(): JSX.Element {
  onMount(formMounted)
  return <div data-testid="ceremony-stub" />
}

function Home(): JSX.Element {
  return <div data-testid="home" />
}

/**
 * Renders the ceremony layout at `address` with a stub child per route and
 * records every navigation. The layout reads each route's real
 * `ceremonyConfig`; only the page content is stubbed.
 */
function renderCeremony(address: string, beforeLeave?: () => void) {
  const history = createMemoryHistory()
  history.set({ value: address, replace: true, scroll: false })
  const navigations: string[] = []
  history.listen(value => navigations.push(value))
  let navigate!: ReturnType<typeof useNavigate>
  let auth!: ReturnType<typeof useAuth>
  function AuthProbe(props: { children?: JSX.Element }): JSX.Element {
    auth = useAuth()
    return <SignInRecoveryGate>{props.children}</SignInRecoveryGate>
  }
  function Root(props: { children?: JSX.Element }): JSX.Element {
    navigate = useNavigate()
    if (beforeLeave)
      useBeforeLeave(beforeLeave)
    return <AuthProvider><AuthProbe>{props.children}</AuthProbe></AuthProvider>
  }
  render(() => (
    <MemoryRouter history={history} root={Root}>
      <Route path="/" component={CeremonyLayout}>
        <Route path="/login" component={Stub} />
        <Route path="/signup" component={Stub} />
        <Route path="/recover-account" component={Stub} />
        <Route path="/recover-account/complete" component={Stub} />
        <Route path="/verify-email" component={Stub} />
        <Route path="/elevate" component={Stub} />
        <Route path="/auth/idp/complete-signup" component={Stub} />
      </Route>
      <Route path="/" component={Home} />
    </MemoryRouter>
  ))
  return {
    auth: () => auth,
    navigate,
    navigations,
  }
}
