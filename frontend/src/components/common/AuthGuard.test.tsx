import { createMemoryHistory, MemoryRouter, Route } from '@solidjs/router'
import { render, screen } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AuthGuard } from '~/components/common/AuthGuard'

// Mock the auth context module
const mockUser = vi.fn()
const mockLoading = vi.fn()
const mockIsAuthenticated = vi.fn()
const mockBootstrapError = vi.fn<() => string | null>(() => null)
const mockRetryBootstrap = vi.fn(async () => {})

vi.mock('~/context/AuthContext', () => ({
  useAuth: () => ({
    user: mockUser,
    loading: mockLoading,
    isAuthenticated: mockIsAuthenticated,
    error: () => null,
    bootstrapError: mockBootstrapError,
    retryBootstrap: mockRetryBootstrap,
    login: async () => {},
    logout: async () => {},
    setAuth: () => {},
  }),
}))

const mockIsSoloMode = vi.fn<() => boolean>(() => false)
const mockIsSetupRequired = vi.fn<() => boolean>(() => false)

vi.mock('~/lib/systemInfo', () => ({
  isSoloMode: () => mockIsSoloMode(),
  isSetupRequired: () => mockIsSetupRequired(),
  isSystemInfoLoaded: () => true,
  isCaptchaEnabled: () => false,
  getCaptchaAlgorithm: () => '',
}))

/**
 * Renders AuthGuard inside a REAL router so the redirects it performs are
 * observable, and records every navigation it causes.
 *
 * `/login` and `/setup` are unguarded stubs, mirroring the real route tree
 * where only the `(app)` layout sits behind the guard — so a redirect chain
 * that bounces through `/login` shows up as an extra `navigations` entry
 * instead of being invisible behind a single final URL.
 */
function renderGuard(options: { path?: string } = {}) {
  const history = createMemoryHistory()
  const path = options.path ?? '/'
  if (path !== '/')
    history.set({ value: path, replace: true, scroll: false })

  // Listen only after the initial entry is in place, so `navigations` holds
  // exactly the redirects the guard itself performed.
  const navigations: string[] = []
  history.listen(value => navigations.push(value))

  const Guarded = () => (
    <AuthGuard>
      <div>Protected Content</div>
    </AuthGuard>
  )

  const result = render(() => (
    <MemoryRouter history={history}>
      <Route path="/" component={Guarded} />
      <Route path="/login" component={() => <div data-testid="login-stub" />} />
      <Route path="/setup" component={() => <div data-testid="setup-stub" />} />
    </MemoryRouter>
  ))

  return { ...result, navigations }
}

describe('authGuard', () => {
  beforeEach(() => {
    mockIsSoloMode.mockReturnValue(false)
    mockIsSetupRequired.mockReturnValue(false)
    mockBootstrapError.mockReturnValue(null)
    mockRetryBootstrap.mockClear()
  })

  it('renders children for an authenticated user', () => {
    mockUser.mockReturnValue({ id: '1', isAdmin: false })
    mockLoading.mockReturnValue(false)
    mockIsAuthenticated.mockReturnValue(true)

    const { navigations } = renderGuard()

    expect(screen.getByText('Protected Content')).toBeInTheDocument()
    expect(navigations).toEqual([])
  })

  it('shows loading while auth is loading', () => {
    mockUser.mockReturnValue(null)
    mockLoading.mockReturnValue(true)
    mockIsAuthenticated.mockReturnValue(false)

    const { navigations } = renderGuard()

    expect(screen.getByText('Loading...')).toBeInTheDocument()
    expect(screen.queryByText('Protected Content')).not.toBeInTheDocument()
    // Nothing is decided until auth resolves.
    expect(navigations).toEqual([])
  })

  it('redirects an unauthenticated visitor to /login with the return path', async () => {
    mockUser.mockReturnValue(null)
    mockLoading.mockReturnValue(false)
    mockIsAuthenticated.mockReturnValue(false)

    const { navigations } = renderGuard()

    expect(await screen.findByTestId('login-stub')).toBeInTheDocument()
    expect(navigations).toEqual(['/login?redirect=%2F'])
  })

  // `/` is the whole guarded app, so what a redirect has to carry back is the
  // QUERY, not a deeper path: `?newWorkspace=true&workerId=…` is a real deep
  // link (AppShell opens the new-workspace dialog off it), and losing it on the
  // login bounce drops the user on a plain home page instead.
  it('preserves the guarded path and query in the login redirect', async () => {
    mockUser.mockReturnValue(null)
    mockLoading.mockReturnValue(false)
    mockIsAuthenticated.mockReturnValue(false)

    const { navigations } = renderGuard({ path: '/?newWorkspace=true' })

    expect(await screen.findByTestId('login-stub')).toBeInTheDocument()
    expect(navigations).toEqual(['/login?redirect=%2F%3FnewWorkspace%3Dtrue'])
  })

  it('sends an unauthenticated visitor straight to /setup when setup is required', async () => {
    mockUser.mockReturnValue(null)
    mockLoading.mockReturnValue(false)
    mockIsAuthenticated.mockReturnValue(false)
    mockIsSetupRequired.mockReturnValue(true)

    const { navigations } = renderGuard()

    expect(await screen.findByTestId('setup-stub')).toBeInTheDocument()
    // Exactly one hop: no `/login` bounce on a fresh install.
    expect(navigations).toEqual(['/setup'])
  })

  it('sends a query-bearing guarded path straight to /setup when setup is required', async () => {
    mockUser.mockReturnValue(null)
    mockLoading.mockReturnValue(false)
    mockIsAuthenticated.mockReturnValue(false)
    mockIsSetupRequired.mockReturnValue(true)

    const { navigations } = renderGuard({ path: '/?newWorkspace=true' })

    expect(await screen.findByTestId('setup-stub')).toBeInTheDocument()
    // Still exactly one hop, and the query is dropped on purpose: a fresh
    // install has no workspace to create one next to.
    expect(navigations).toEqual(['/setup'])
  })

  // Solo mode has no login form, so a redirect is the wrong answer here — but
  // so was the loading fallback this used to land on. `restoreSession` records
  // NO bootstrapError for an Unauthenticated reply (it is the ordinary "no
  // session yet" answer everywhere else), so solo + unauthenticated fell
  // through every arm and spun forever with nothing to click. In solo mode the
  // hub authenticates every request, so that reply is a transport or config
  // failure and has to be reported like any other.
  it('reports rather than spins when a solo hub answers with no session', async () => {
    mockUser.mockReturnValue(null)
    mockLoading.mockReturnValue(false)
    mockIsAuthenticated.mockReturnValue(false)
    mockIsSoloMode.mockReturnValue(true)

    const { navigations } = renderGuard()

    expect(await screen.findByTestId('auth-bootstrap-error')).toBeInTheDocument()
    expect(screen.queryByText('Loading...')).not.toBeInTheDocument()
    expect(screen.queryByText('Protected Content')).not.toBeInTheDocument()
    // Still no redirect: there is no login form to send them to.
    expect(navigations).toEqual([])
  })

  it('offers the same retry for a solo hub with no session', async () => {
    mockUser.mockReturnValue(null)
    mockLoading.mockReturnValue(false)
    mockIsAuthenticated.mockReturnValue(false)
    mockIsSoloMode.mockReturnValue(true)

    renderGuard()

    ;(await screen.findByRole('button', { name: /retry/i })).click()
    expect(mockRetryBootstrap).toHaveBeenCalledTimes(1)
  })

  // A failed bootstrap (hub unreachable / erroring) is NOT a missing session.
  // It used to be swallowed into an empty catch, so it was indistinguishable
  // from "not logged in": in solo mode -- where there is no login to fall back
  // to -- the guard showed its loading fallback forever, and if loadSystemInfo
  // had also failed, isSoloMode() defaults to false and the visitor was sent to
  // a login form no credentials could satisfy. Both were unrecoverable and
  // silent. The guard must report it and offer a way out instead.
  it('reports a failed bootstrap instead of redirecting or spinning', async () => {
    mockLoading.mockReturnValue(false)
    mockUser.mockReturnValue(null)
    mockIsAuthenticated.mockReturnValue(false)
    mockBootstrapError.mockReturnValue('connection refused')

    const { navigations } = renderGuard()

    const panel = await screen.findByTestId('auth-bootstrap-error')
    expect(panel).toBeInTheDocument()
    expect(panel).toHaveTextContent('connection refused')
    expect(screen.queryByText('Loading...')).not.toBeInTheDocument()
    expect(navigations).toEqual([])
  })

  it('offers a retry that re-runs the bootstrap', async () => {
    mockLoading.mockReturnValue(false)
    mockUser.mockReturnValue(null)
    mockIsAuthenticated.mockReturnValue(false)
    mockBootstrapError.mockReturnValue('connection refused')

    renderGuard()

    ;(await screen.findByRole('button', { name: /retry/i })).click()
    expect(mockRetryBootstrap).toHaveBeenCalledTimes(1)
  })

  // Solo mode is the case with no fallback at all: without the panel above,
  // this state is a permanent spinner with nothing to click.
  it('reports a failed bootstrap in solo mode too', async () => {
    mockIsSoloMode.mockReturnValue(true)
    mockLoading.mockReturnValue(false)
    mockUser.mockReturnValue(null)
    mockIsAuthenticated.mockReturnValue(false)
    mockBootstrapError.mockReturnValue('hub unreachable')

    const { navigations } = renderGuard()

    expect(await screen.findByTestId('auth-bootstrap-error')).toBeInTheDocument()
    expect(navigations).toEqual([])
  })
})
