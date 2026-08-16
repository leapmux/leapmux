import { createMemoryHistory, MemoryRouter, Route } from '@solidjs/router'
/// <reference types="vitest/globals" />
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { CaptchaProvider } from '~/generated/leapmux/v1/auth_pb'
import { mockLoadOAuthProviders, resetSystemInfoMock, setSystemInfoMock } from '~/test-support/systemInfoMock'

import { LoginPage } from './LoginPage'

vi.mock('~/api/clients', () => ({
  authClient: {
    login: vi.fn(),
    logout: vi.fn(),
    getCurrentUser: vi.fn(),
  },
}))

// The shared systemInfo mock: every getter reads through a real Solid
// signal, exactly as the production module derives them from its
// snapshot signal, so a runtime toggle re-evaluates the gates that read
// it (a plain vi.fn would freeze the first answer inside a <Show>).
// Tests patch values with setSystemInfoMock and reset in beforeEach.
vi.mock('~/lib/systemInfo', async () => {
  const m = await import('~/test-support/systemInfoMock')
  return m.systemInfoMock
})

// The fake widget holds its payload in a signal the test controls, so a
// test can keep the form unsolved (button disabled) and release it. The
// unavailable callback is captured so a test can simulate the hub
// answering "no challenge" (captcha disabled at runtime).
const [mockCaptchaPayload, setMockCaptchaPayload] = createSignal<string | null>(null)
let mockCaptchaUnavailable: (() => void) | undefined
vi.mock('~/components/common/CaptchaField', async () => {
  const { createEffect } = await import('solid-js')
  return {
    CaptchaField: (props: { action: string, onPayload: (p: string | null) => void, onUnavailable: () => void }) => {
      // Captured for the stand-down test; the primitive's callback is a
      // stable closure, so an untracked read is fine.
      /* eslint-disable solid/reactivity -- stable callback captured for tests */
      mockCaptchaUnavailable = props.onUnavailable
      /* eslint-enable solid/reactivity */
      createEffect(() => props.onPayload(mockCaptchaPayload()))
      return <div data-testid="captcha-field" data-action={props.action} />
    },
  }
})
vi.mock('~/components/common/CaptchaHoneypot', () => ({
  CaptchaHoneypot: (props: { value: string, onInput: (v: string) => void }) => (
    <input
      data-testid="captcha-honeypot"
      type="text"
      name="website"
      value={props.value}
      onInput={e => props.onInput(e.currentTarget.value)}
    />
  ),
}))

const mockLogin = vi.fn()
const mockUser = vi.fn<() => { id: string, username: string } | null>(() => null)
// `loading` is a real signal so a test can hold the page in the pre-bootstrap
// state and then release it, which is the whole shape of the race below.
const [authLoading, setAuthLoading] = createSignal(false)
vi.mock('~/context/AuthContext', () => ({
  useAuth: () => ({
    user: () => mockUser(),
    loading: () => authLoading(),
    error: () => null,
    login: mockLogin,
    logout: vi.fn(),
    setAuth: vi.fn(),
    isAuthenticated: () => false,
  }),
  AuthProvider: (props: { children: unknown }) => <>{props.children}</>,
}))

function renderLoginPage(initialPath = '/login') {
  const history = createMemoryHistory()
  history.set({ value: initialPath, replace: true, scroll: false })
  return render(() => (
    <MemoryRouter history={history}>
      <Route path="/login" component={LoginPage} />
      <Route path="/" component={() => <div data-testid="app-home" />} />
      <Route path="/setup" component={() => <div data-testid="setup-page" />} />
    </MemoryRouter>
  ))
}

describe('loginPage', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    resetSystemInfoMock()
    setMockCaptchaPayload(null)
    mockCaptchaUnavailable = undefined
    setAuthLoading(false)
  })

  // These three pin the bootstrap race. The system-info getters are plain
  // module reads whose pre-fetch values are fabrications (soloMode = false,
  // setupRequired = false), so sampling them before bootstrap resolves is
  // sampling a guess. This used to run in onMount, which fires once and never
  // re-checks: on any load that beat the RPC, a solo-mode visitor was stranded
  // on a credential form that cannot succeed, and the signup link was pinned
  // off. The redirects must therefore wait for auth.loading() to clear and
  // must still fire afterwards.
  // Each of these models the getter faithfully: it answers the module's
  // fabricated default while bootstrap is in flight, and the truth afterwards.
  // Reading it early therefore yields the WRONG answer, not merely an early
  // one -- which is what made the onMount version fail silently and forever.
  it('waits for bootstrap before deciding, then redirects a solo hub', async () => {
    setAuthLoading(true)

    renderLoginPage()

    // Bootstrap is still in flight, so nothing has been decided yet.
    expect(screen.queryByTestId('app-home')).not.toBeInTheDocument()

    setSystemInfoMock({ soloMode: true })
    setAuthLoading(false)

    expect(await screen.findByTestId('app-home')).toBeInTheDocument()
  })

  it('redirects to setup once bootstrap reports a fresh install', async () => {
    setAuthLoading(true)

    renderLoginPage()
    expect(screen.queryByTestId('setup-page')).not.toBeInTheDocument()

    setSystemInfoMock({ setupRequired: true })
    setAuthLoading(false)

    expect(await screen.findByTestId('setup-page')).toBeInTheDocument()
  })

  it('shows the signup link once bootstrap enables it', async () => {
    setSystemInfoMock({ signupEnabled: false })
    setAuthLoading(true)

    renderLoginPage()
    expect(screen.queryByRole('link', { name: 'Sign up' })).not.toBeInTheDocument()

    setSystemInfoMock({ signupEnabled: true })
    setAuthLoading(false)

    await vi.waitFor(() => {
      expect(screen.getByRole('link', { name: 'Sign up' })).toBeInTheDocument()
    })
  })

  it('renders email/password form when no oauth providers', async () => {
    renderLoginPage()

    await vi.waitFor(() => {
      expect(screen.getByLabelText('Username')).toBeInTheDocument()
    })
    expect(screen.getByLabelText('Password')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Sign in' })).toBeInTheDocument()
    expect(screen.queryByText(/Sign in with/)).not.toBeInTheDocument()
  })

  // Autofill lands before the bootstrap effect picks a field to focus, so
  // the values must be in place while bootstrap is still held.
  it('focuses the first empty input: the password when only the username is autofilled', async () => {
    setAuthLoading(true)
    renderLoginPage()

    const username = screen.getByLabelText('Username') as HTMLInputElement
    const password = screen.getByLabelText('Password') as HTMLInputElement
    username.value = 'admin' // the browser's username-only autofill

    setAuthLoading(false)
    await vi.waitFor(() => {
      expect(document.activeElement).toBe(password)
    })
  })

  it('focuses the username when both inputs are empty', async () => {
    setAuthLoading(true)
    renderLoginPage()

    const username = screen.getByLabelText('Username') as HTMLInputElement

    setAuthLoading(false)
    await vi.waitFor(() => {
      expect(document.activeElement).toBe(username)
    })
  })

  it('renders oauth buttons when providers are configured', async () => {
    mockLoadOAuthProviders.mockResolvedValue([
      { id: 'p1', name: 'Google', providerType: 'oidc', loginUrl: '/auth/oauth/p1/login' },
      { id: 'p2', name: 'GitHub', providerType: 'github', loginUrl: '/auth/oauth/p2/login' },
    ])

    renderLoginPage()

    await vi.waitFor(() => {
      expect(screen.getByText(/Sign in with Google/)).toBeInTheDocument()
    })
    expect(screen.getByText(/Sign in with GitHub/)).toBeInTheDocument()
    expect(screen.getByText('or')).toBeInTheDocument()
    expect(screen.getByLabelText('Username')).toBeInTheDocument()
  })

  it('oauth button links to correct login url', async () => {
    mockLoadOAuthProviders.mockResolvedValue([
      { id: 'p1', name: 'TestProvider', providerType: 'oidc', loginUrl: '/auth/oauth/p1/login' },
    ])

    renderLoginPage()

    await vi.waitFor(() => {
      expect(screen.getByText(/Sign in with TestProvider/)).toBeInTheDocument()
    })

    const link = screen.getByText(/Sign in with TestProvider/).closest('a')
    expect(link).toHaveAttribute('href', '/auth/oauth/p1/login')
  })

  it('shows signup link when signup is enabled', async () => {
    setSystemInfoMock({ signupEnabled: true })

    renderLoginPage()

    await vi.waitFor(() => {
      expect(screen.getByText('Sign up')).toBeInTheDocument()
    })
  })

  it('renders provider with long name correctly', async () => {
    mockLoadOAuthProviders.mockResolvedValue([
      { id: 'p1', name: 'Corporate Azure Active Directory', providerType: 'oidc', loginUrl: '/auth/oauth/p1/login' },
    ])

    renderLoginPage()

    await vi.waitFor(() => {
      expect(screen.getByText(/Sign in with Corporate Azure Active Directory/)).toBeInTheDocument()
    })
  })

  it('disables Sign in button when username is empty', async () => {
    renderLoginPage()
    await vi.waitFor(() => {
      expect(screen.getByLabelText('Username')).toBeInTheDocument()
    })

    fireEvent.input(screen.getByLabelText('Password'), { target: { value: 'secret' } })
    expect(screen.getByRole('button', { name: 'Sign in' })).toBeDisabled()
  })

  it('disables Sign in button when password is empty', async () => {
    renderLoginPage()
    await vi.waitFor(() => {
      expect(screen.getByLabelText('Username')).toBeInTheDocument()
    })

    fireEvent.input(screen.getByLabelText('Username'), { target: { value: 'alice' } })
    expect(screen.getByRole('button', { name: 'Sign in' })).toBeDisabled()
  })

  it('disables Sign in button when both fields are empty', async () => {
    renderLoginPage()
    await vi.waitFor(() => {
      expect(screen.getByRole('button', { name: 'Sign in' })).toBeInTheDocument()
    })

    expect(screen.getByRole('button', { name: 'Sign in' })).toBeDisabled()
  })

  it('enables Sign in button when both fields have values', async () => {
    renderLoginPage()
    await vi.waitFor(() => {
      expect(screen.getByLabelText('Username')).toBeInTheDocument()
    })

    fireEvent.input(screen.getByLabelText('Username'), { target: { value: 'alice' } })
    fireEvent.input(screen.getByLabelText('Password'), { target: { value: 'secret' } })
    expect(screen.getByRole('button', { name: 'Sign in' })).toBeEnabled()
  })

  it('requires a solved captcha before Sign in unlocks when the hub enables captcha', async () => {
    setSystemInfoMock({ captchaEnabled: true })
    setMockCaptchaPayload(null)
    renderLoginPage()
    await vi.waitFor(() => {
      expect(screen.getByLabelText('Username')).toBeInTheDocument()
    })
    fireEvent.input(screen.getByLabelText('Username'), { target: { value: 'alice' } })
    fireEvent.input(screen.getByLabelText('Password'), { target: { value: 'secret' } })

    // Captcha enabled and unsolved: the button stays disabled with both
    // credentials filled — the disable half of the predicate the old test
    // never drove.
    expect(screen.getByTestId('captcha-field')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Sign in' })).toBeDisabled()

    // The widget reports a solve: the button unlocks and the payload and
    // honeypot ride on the login call.
    setMockCaptchaPayload('ZmFrZS1wYXlsb2Fk')
    await vi.waitFor(() => {
      expect(screen.getByRole('button', { name: 'Sign in' })).toBeEnabled()
    })

    mockLogin.mockImplementation(() => Promise.resolve())
    fireEvent.click(screen.getByRole('button', { name: 'Sign in' }))
    await vi.waitFor(() => {
      expect(mockLogin).toHaveBeenCalledWith('alice', 'secret', { captchaPayload: 'ZmFrZS1wYXlsb2Fk', honeypot: '' })
    })
  })

  it('keeps Sign in disabled while bootstrap has not answered the captcha question', async () => {
    // Fail closed: submitting against an unknown captcha policy would send
    // an empty payload the hub denies, so the button waits for the answer.
    setSystemInfoMock({ loaded: false, captchaEnabled: false })
    renderLoginPage()
    await vi.waitFor(() => {
      expect(screen.getByLabelText('Username')).toBeInTheDocument()
    })
    fireEvent.input(screen.getByLabelText('Username'), { target: { value: 'alice' } })
    fireEvent.input(screen.getByLabelText('Password'), { target: { value: 'secret' } })
    // Both credentials are filled; the only blocker is the unanswered
    // captcha question.
    expect(screen.getByRole('button', { name: 'Sign in' })).toBeDisabled()

    // The mock's loaded flag is a real signal, so the flip alone re-runs
    // the disabled expression.
    setSystemInfoMock({ loaded: true })
    await vi.waitFor(() => {
      expect(screen.getByRole('button', { name: 'Sign in' })).toBeEnabled()
    })
  })

  it('passes the login action to the captcha field under an external provider', async () => {
    // The dispatcher test covers provider switching; this pins the page's
    // half of the contract — with Turnstile selected (site key present),
    // the field it renders is told which procedure the token is for. A
    // wrong action would make the hub's siteverify action check deny every
    // login token even though the widget solved fine.
    setSystemInfoMock({ captchaEnabled: true, captchaProvider: CaptchaProvider.TURNSTILE, captchaSiteKey: '1x00000000000000000000AA' })
    renderLoginPage()
    await vi.waitFor(() => {
      expect(screen.getByTestId('captcha-field')).toBeInTheDocument()
    })
    expect(screen.getByTestId('captcha-field')).toHaveAttribute('data-action', 'login')
  })

  it('stands down and unlocks when the hub reports no challenge', async () => {
    // The admin disabled captcha after the page loaded: the widget's
    // fetch answers "no challenge" and the form must lift the requirement
    // instead of dead-locking on a solve that cannot arrive.
    setSystemInfoMock({ captchaEnabled: true })
    setMockCaptchaPayload(null)
    renderLoginPage()
    await vi.waitFor(() => {
      expect(screen.getByTestId('captcha-field')).toBeInTheDocument()
    })
    fireEvent.input(screen.getByLabelText('Username'), { target: { value: 'alice' } })
    fireEvent.input(screen.getByLabelText('Password'), { target: { value: 'secret' } })
    expect(screen.getByRole('button', { name: 'Sign in' })).toBeDisabled()

    mockCaptchaUnavailable?.()
    await vi.waitFor(() => {
      expect(screen.getByRole('button', { name: 'Sign in' })).toBeEnabled()
    })
  })

  it('sends whatever the honeypot captured with the login attempt', async () => {
    mockLogin.mockImplementation(() => Promise.resolve())
    renderLoginPage()
    await vi.waitFor(() => {
      expect(screen.getByLabelText('Username')).toBeInTheDocument()
    })
    fireEvent.input(screen.getByLabelText('Username'), { target: { value: 'alice' } })
    fireEvent.input(screen.getByLabelText('Password'), { target: { value: 'secret' } })
    // An autofill heuristic dropping a value into the hidden input must
    // reach the request — the server checks it even with captcha off.
    fireEvent.input(screen.getByTestId('captcha-honeypot'), { target: { value: 'http://spam.example' } })
    fireEvent.click(screen.getByRole('button', { name: 'Sign in' }))
    await vi.waitFor(() => {
      expect(mockLogin).toHaveBeenCalledWith('alice', 'secret', { captchaPayload: '', honeypot: 'http://spam.example' })
    })
  })

  it('keeps button disabled after successful login', async () => {
    mockLogin.mockImplementation(() => {
      mockUser.mockReturnValue({ id: 'u1', username: 'alice' })
      return Promise.resolve()
    })

    renderLoginPage()

    await vi.waitFor(() => {
      expect(screen.getByLabelText('Username')).toBeInTheDocument()
    })

    fireEvent.input(screen.getByLabelText('Username'), { target: { value: 'alice' } })
    fireEvent.input(screen.getByLabelText('Password'), { target: { value: 'secret' } })

    const button = screen.getByRole('button', { name: 'Sign in' })
    fireEvent.click(button)

    // Wait for login to complete.
    await vi.waitFor(() => {
      expect(mockLogin).toHaveBeenCalledOnce()
    })

    // Flush microtasks so the async handler finishes.
    await new Promise(r => setTimeout(r, 0))

    // The button should never revert to 'Sign in' after a successful login.
    expect(screen.queryByRole('button', { name: 'Sign in' })).not.toBeInTheDocument()
  })

  it('redirects to / after successful login when no redirect param', async () => {
    mockLogin.mockImplementation(() => {
      mockUser.mockReturnValue({ id: 'u1', username: 'alice' })
      return Promise.resolve()
    })

    renderLoginPage()

    await vi.waitFor(() => {
      expect(screen.getByLabelText('Username')).toBeInTheDocument()
    })

    fireEvent.input(screen.getByLabelText('Username'), { target: { value: 'alice' } })
    fireEvent.input(screen.getByLabelText('Password'), { target: { value: 'secret' } })
    fireEvent.click(screen.getByRole('button', { name: 'Sign in' }))

    // Flat user-owned home is `/` (no org slug).
    expect(await screen.findByTestId('app-home')).toBeInTheDocument()
  })
})
