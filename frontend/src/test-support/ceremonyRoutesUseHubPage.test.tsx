import type { JSX } from 'solid-js'
import type { CeremonyConfig } from '~/routes/(ceremonies)'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { createMemoryHistory, MemoryRouter, Route } from '@solidjs/router'
import { cleanup, render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import CeremonyLayout, { ceremonyConfigFor, ceremonyConfigs } from '~/routes/(ceremonies)'

const ceremoniesInBrowser = vi.hoisted(() => vi.fn<() => boolean>(() => false))
vi.mock('~/lib/hubSite', () => ({
  ceremoniesRunInBrowser: () => ceremoniesInBrowser(),
  hubOrigin: () => 'https://hub.example',
  hubPageUrl: (path: string) => `https://hub.example${path}`,
  openHubPage: vi.fn(async () => {}),
}))

const [currentUser, setCurrentUser] = createSignal<{ id: string, username: string } | null>(null)
vi.mock('~/context/AuthContext', () => ({
  useAuth: () => ({
    user: () => currentUser(),
    loading: () => false,
    logout: vi.fn(async () => {}),
    refreshUser: vi.fn(async () => {}),
    adoptBrowserSignIn: vi.fn(async () => ({ outcome: 'adopted' as const, verificationRequired: false })),
    signInRecovery: () => null,
  }),
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

vi.mock('~/api/clients', () => ({
  authClient: {
    getPendingOAuthSignup: vi.fn(),
    completeOAuthSignup: vi.fn(),
    login: vi.fn(),
    logout: vi.fn(),
    getCurrentUser: vi.fn(),
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

describe('CeremonyLayout', () => {
  beforeEach(() => {
    ceremoniesInBrowser.mockReturnValue(false)
    setCurrentUser(null)
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
    setCurrentUser({ id: 'u1', username: 'alice' })
    renderCeremony('/recover-account/complete')

    expect(await screen.findByTestId('signed-out-only-explain')).toBeInTheDocument()
    expect(screen.queryByText('Continue in your browser')).toBeNull()
  })

  it('runs the default redirect gate behind the remote-Hub fallback', async () => {
    ceremoniesInBrowser.mockReturnValue(true)
    setCurrentUser({ id: 'u1', username: 'alice' })
    const { navigations } = renderCeremony('/recover-account')

    await vi.waitFor(() => expect(navigations).toEqual(['/']))
    expect(screen.queryByText('Continue in your browser')).toBeNull()
  })

  it('renders the routes without a gate for a signed-in visitor', () => {
    for (const address of ['/verify-email', '/elevate', '/auth/idp/complete-signup']) {
      setCurrentUser({ id: 'u1', username: 'alice' })
      const { navigations } = renderCeremony(address)
      expect(screen.getByTestId('ceremony-stub'), address).toBeInTheDocument()
      expect(navigations, address).toEqual([])
      cleanup()
    }
  })
})

function Stub(): JSX.Element {
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
function renderCeremony(address: string) {
  const history = createMemoryHistory()
  history.set({ value: address, replace: true, scroll: false })
  const navigations: string[] = []
  history.listen(value => navigations.push(value))
  render(() => (
    <MemoryRouter history={history}>
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
  return { navigations }
}
