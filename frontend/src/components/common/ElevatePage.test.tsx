import type { Timestamp } from '@bufbuild/protobuf/wkt'
import { timestampFromDate } from '@bufbuild/protobuf/wkt'
import { createMemoryHistory, MemoryRouter, Route } from '@solidjs/router'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { ElevatePage } from '~/components/common/ElevatePage'
import { resetSystemInfoMock, setSystemInfoMock } from '~/test-support/systemInfoMock'

const mockUser = vi.fn()
const mockLoading = vi.fn<() => boolean>(() => false)
const mockIsAuthenticated = vi.fn<() => boolean>(() => true)

/**
 * A REAL signal, not a `vi.fn` that answers a fixed value.
 *
 * The page decides from this signal, and a successful ceremony writes it. With
 * a constant stub the ceremony's write changes nothing, so the memo recomputes
 * once and the double navigation this suite exists to catch cannot happen in a
 * test at all.
 */
const [mockElevationExpiresAt, mockSetElevationExpiresAt] = createSignal<Timestamp | undefined>()

/**
 * Stands in for `AuthState.elevateWithPassword`, which runs the RPC AND adopts
 * the returned deadline. So the fake adopts one too: the ceremony's own write
 * and the form's `onElevated` land in two different microtasks, and Solid does
 * not batch across an await.
 */
const mockElevateWithPassword = vi.fn<(currentPassword: string) => Promise<void>>()

vi.mock('~/context/AuthContext', () => ({
  useAuth: () => ({
    user: mockUser,
    loading: mockLoading,
    isAuthenticated: mockIsAuthenticated,
    elevationExpiresAt: mockElevationExpiresAt,
    elevateWithPassword: (...args: [string]) => mockElevateWithPassword(...args),
    elevateWithPasskey: vi.fn(),
    refreshUser: async () => {},
  }),
}))

vi.mock('~/lib/systemInfo', async () => {
  const m = await import('~/test-support/systemInfoMock')
  return m.systemInfoMock
})

/** A ceremony that succeeds and adopts the window the hub granted. */
function elevationGranted(): () => Promise<void> {
  return async () => {
    mockSetElevationExpiresAt(timestampFromDate(new Date(Date.now() + 60_000)))
  }
}

const assign = vi.fn()

/**
 * Renders /elevate inside a REAL router, so the redirects it performs are
 * observable rather than inferred. Full-document navigations (the hub's own
 * /auth/ routes) land on the stubbed `location.assign` instead.
 */
function renderElevate(path: string) {
  const history = createMemoryHistory()
  history.set({ value: path, replace: true, scroll: false })
  const navigations: string[] = []
  history.listen(value => navigations.push(value))

  render(() => (
    <MemoryRouter history={history}>
      <Route path="/elevate" component={ElevatePage} />
      <Route path="/" component={() => <div>Home</div>} />
      <Route path="/login" component={() => <div>Login</div>} />
    </MemoryRouter>
  ))
  return { history, navigations }
}

describe('elevatePage', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    // The passkey-availability tests below patch the snapshot, and it is
    // module state: without the reset the next test inherits it.
    resetSystemInfoMock()
    vi.stubGlobal('location', { assign })
    mockLoading.mockReturnValue(false)
    mockIsAuthenticated.mockReturnValue(true)
    // A signal survives `clearAllMocks`, so this hook resets it by hand.
    mockSetElevationExpiresAt(undefined)
    mockElevateWithPassword.mockResolvedValue(undefined)
    mockUser.mockReturnValue({
      id: 'user-1',
      username: 'alice',
      passwordSet: true,
      passkeyCount: 0,
      oauthProviders: [],
    })
  })

  it('prompts when the session holds no elevation', async () => {
    renderElevate('/elevate?redirect=%2F')
    expect(await screen.findByTestId('elevate-card')).toBeInTheDocument()
    expect(screen.getByTestId('elevate-password')).toBeInTheDocument()
  })

  // The FIRST of the two independent loop-prevention layers the hub's
  // consent gate relies on. Without it, a browser that arrives already
  // elevated sits on a prompt for a window it already holds.
  it('leaves immediately when the session is already elevated', async () => {
    mockSetElevationExpiresAt(timestampFromDate(new Date(Date.now() + 60_000)))
    const { navigations } = renderElevate('/elevate?redirect=%2F')
    await vi.waitFor(() => {
      expect(navigations).toContain('/')
    })
    expect(screen.queryByTestId('elevate-card')).not.toBeInTheDocument()
  })

  it('returns to a hub route with a full-document load', async () => {
    mockElevateWithPassword.mockImplementation(elevationGranted())
    renderElevate(`/elevate?redirect=${encodeURIComponent('/auth/cli/start?state=s&elevated=1')}`)

    fireEvent.input(await screen.findByTestId('elevate-password'), { target: { value: 'secret' } })
    fireEvent.click(screen.getByTestId('elevate-password-submit'))

    await vi.waitFor(() => {
      // A router navigate would render the SPA's 404 page while the CLI
      // waits for a consent screen nobody ever sees.
      expect(assign).toHaveBeenCalledWith('/auth/cli/start?state=s&elevated=1')
    })
    expect(mockElevateWithPassword).toHaveBeenCalledWith('secret')
    // EXACTLY once, and a success writes TWO signals to get here: the adopted
    // deadline inside the ceremony, then `proven` in the continuation after the
    // await. Solid does not batch across an await, so the memo recomputes twice
    // -- and a memo that compares two fresh objects with `===` notifies on
    // both. On a hub route that is two full-document loads of a single-use
    // consent URL.
    //
    // Wait for the second call before this reads the count.
    await new Promise(done => setTimeout(done, 20))
    expect(assign).toHaveBeenCalledTimes(1)
  })

  // A success the hub reports without a deadline must still leave the page.
  // The `proven` signal covers the shape a timestamp cannot: without it the
  // memo stays on 'prompt' and strands the user on a form they answered.
  it('leaves even when the response carries no deadline', async () => {
    mockElevateWithPassword.mockResolvedValue(undefined)
    const { navigations } = renderElevate('/elevate?redirect=%2F')

    fireEvent.input(await screen.findByTestId('elevate-password'), { target: { value: 'secret' } })
    fireEvent.click(screen.getByTestId('elevate-password-submit'))

    await vi.waitFor(() => {
      expect(navigations).toContain('/')
    })
  })

  it('sends a signed-out visitor to sign in and back again', async () => {
    mockIsAuthenticated.mockReturnValue(false)
    const { navigations } = renderElevate('/elevate?redirect=%2F')
    await vi.waitFor(() => {
      expect(navigations.some(n => n.startsWith('/login?redirect='))).toBe(true)
    })
    // The return address is the /elevate page itself, carrying the original
    // destination: signing in must not lose the address the user asked for.
    const login = navigations.find(n => n.startsWith('/login?redirect='))!
    expect(decodeURIComponent(login)).toContain('/elevate?redirect=')
  })

  it('refuses a redirect that could leave the origin', async () => {
    mockElevateWithPassword.mockResolvedValue(undefined)
    const { navigations } = renderElevate(`/elevate?redirect=${encodeURIComponent('https://evil.example/')}`)

    fireEvent.input(await screen.findByTestId('elevate-password'), { target: { value: 'secret' } })
    fireEvent.click(screen.getByTestId('elevate-password-submit'))
    await vi.waitFor(() => {
      expect(navigations).toContain('/')
    })
    expect(assign).not.toHaveBeenCalled()
  })

  it('says so when the account has nothing to verify with', async () => {
    mockUser.mockReturnValue({
      id: 'user-1',
      username: 'alice',
      passwordSet: false,
      passkeyCount: 0,
      oauthProviders: [],
    })
    renderElevate('/elevate?redirect=%2F')
    const message = await screen.findByTestId('elevate-impossible')
    // This account CAN recover: the hub admits a first password on a recent
    // sign-in rather than on an elevation, so the instruction is to sign in
    // again -- not "set a password", which is the thing that the hub refuses.
    expect(message).toHaveTextContent(/sign in again/i)
  })

  // TWO hub answers, and the form mirrors both and derives nothing of its
  // own. `mayElevateThroughAProvider` is the ACCOUNT rule, from the same
  // predicate the OAuth re-authentication leg enforces
  // (accountElevatesOnlyThroughAProvider, in elevation_service.go --
  // TestAccountElevatesOnlyThroughAProvider covers the rule itself).
  // `enabled` is the per-link fact an administrator controls. The form used
  // to re-derive the account rule from passwordSet, passkeyCount and
  // the passkey blocker, which was a second source of truth for an authorization
  // decision.
  //
  // Offering an option that the hub refuses is a dead end rather than a wasted
  // click: it is a full-document navigation out of the app to a bare 403
  // page with no route back, and it hides the copy that states the remedy.
  it('offers no option when the hub marks the only linked provider unusable', async () => {
    setSystemInfoMock({ passkeyBlocker: 'origin-not-allowed' })
    mockUser.mockReturnValue({
      id: 'user-1',
      username: 'alice',
      passwordSet: false,
      passkeyCount: 1,
      mayElevateThroughAProvider: false,
      oauthProviders: [{ id: 'github', name: 'GitHub', enabled: true }],
    })

    renderElevate('/elevate?redirect=%2F')

    const message = await screen.findByTestId('elevate-impossible')
    expect(message).toHaveTextContent(/administrator/i)
    expect(screen.queryByTestId('elevate-oauth-github')).not.toBeInTheDocument()
  })

  // A DISABLED provider is still listed -- the owner must be able to detach
  // it -- and must still not be offered as an option, because both OAuth legs
  // answer 403 "provider disabled".
  it('offers only the enabled links, and no other', async () => {
    setSystemInfoMock({ passkeyBlocker: 'origin-not-allowed' })
    mockUser.mockReturnValue({
      id: 'user-1',
      username: 'alice',
      passwordSet: false,
      passkeyCount: 0,
      mayElevateThroughAProvider: true,
      oauthProviders: [
        { id: 'github', name: 'GitHub', enabled: false },
        { id: 'okta', name: 'Okta', enabled: true },
      ],
    })

    renderElevate('/elevate?redirect=%2F')

    expect(await screen.findByTestId('elevate-oauth-okta')).toBeInTheDocument()
    expect(screen.queryByTestId('elevate-oauth-github')).not.toBeInTheDocument()
    expect(screen.queryByTestId('elevate-impossible')).not.toBeInTheDocument()
  })

  // The account flag OUTRANKS what the form could infer, and it outranks the
  // per-link one too. This account holds a password -- the one shape where it
  // chose a secret and the provider option would let somebody past it without
  // knowing it -- so the hub refuses the option and the form must not offer it,
  // whatever the provider is and however enabled it is.
  it('hides an enabled provider when the account may not use one', async () => {
    setSystemInfoMock({ passkeyBlocker: 'origin-not-allowed' })
    mockUser.mockReturnValue({
      id: 'user-1',
      username: 'alice',
      passwordSet: true,
      passkeyCount: 0,
      mayElevateThroughAProvider: false,
      oauthProviders: [{ id: 'okta', name: 'Okta', enabled: true }],
    })

    renderElevate('/elevate?redirect=%2F')

    expect(await screen.findByTestId('elevate-password')).toBeInTheDocument()
    expect(screen.queryByTestId('elevate-oauth-okta')).not.toBeInTheDocument()
  })

  // And the reverse: an account with no password and no passkey elevates
  // through ANY linked provider, GitHub included, because the provider IS
  // its sign-in credential. A form that tried to infer the option from the
  // provider's protocol capability would hide this one.
  it('offers a provider the hub allowed', async () => {
    setSystemInfoMock({ passkeyBlocker: 'origin-not-allowed' })
    mockUser.mockReturnValue({
      id: 'user-1',
      username: 'alice',
      passwordSet: false,
      passkeyCount: 0,
      mayElevateThroughAProvider: true,
      oauthProviders: [{ id: 'github', name: 'GitHub', enabled: true }],
    })

    renderElevate('/elevate?redirect=%2F')

    expect(await screen.findByTestId('elevate-oauth-github')).toBeInTheDocument()
    expect(screen.queryByTestId('elevate-impossible')).not.toBeInTheDocument()
  })

  it('stops promising a remedy when a passkey is the only factor and the hub cannot run it', async () => {
    setSystemInfoMock({ passkeyBlocker: 'origin-not-allowed' })
    mockUser.mockReturnValue({
      id: 'user-1',
      username: 'alice',
      passwordSet: false,
      passkeyCount: 1,
      oauthProviders: [],
    })

    renderElevate('/elevate?redirect=%2F')

    const message = await screen.findByTestId('elevate-impossible')
    expect(message).toHaveTextContent(/administrator/i)
    // The old copy sent the user to Preferences to set a password, which
    // this account cannot do without the elevation the hub refuses it.
    expect(message).not.toHaveTextContent(/Set a password in Preferences/i)
  })
})
