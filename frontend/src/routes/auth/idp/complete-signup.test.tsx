import type { MemoryHistory } from '@solidjs/router'
import type { SignInRecovery } from '~/context/AuthContext'

import { createMemoryHistory, MemoryRouter, Route } from '@solidjs/router'
/// <reference types="vitest/globals" />
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { SignInRecoveryGate } from '~/components/common/SignInRecoveryGate'
import { settle } from '~/test-support/async'
import OAuthCompleteSignupPage from './complete-signup'

const mockGetPendingOAuthSignup = vi.fn<(...args: unknown[]) => Promise<Record<string, unknown>>>()
const mockCompleteOAuthSignup = vi.fn<(...args: unknown[]) => Promise<Record<string, unknown>>>()

vi.mock('~/api/clients', () => ({
  authClient: {
    getPendingOAuthSignup: (...args: unknown[]) => mockGetPendingOAuthSignup(...args),
    completeOAuthSignup: (...args: unknown[]) => mockCompleteOAuthSignup(...args),
    login: vi.fn(),
    logout: vi.fn(),
    getCurrentUser: vi.fn(),
  },
}))

const mockIsCaptchaEnabled = vi.fn<() => boolean>(() => false)
const mockGetCaptchaProvider = vi.fn<() => number>(() => 1) // CaptchaProvider.ALTCHA
// The hub requires an address exactly when it can verify one, so the
// field follows the same rule.
const mockIsEmailEnabled = vi.fn(() => false)
// False while the bootstrap's system info load is still out.
const mockIsSystemInfoLoaded = vi.fn(() => true)

vi.mock('~/lib/systemInfo', () => ({
  isSoloMode: () => false,
  loadSystemInfo: () => Promise.resolve(),
  isSignupEnabled: () => false,
  loadOAuthProviders: () => Promise.resolve([]),
  isSystemInfoLoaded: () => mockIsSystemInfoLoaded(),
  isEmailEnabled: () => mockIsEmailEnabled(),
  isCaptchaEnabled: () => mockIsCaptchaEnabled(),
  isCaptchaUnsolvableHere: () => false,
  getAltchaAlgorithm: () => '',
  getCaptchaProvider: () => mockGetCaptchaProvider(),
  getCaptchaSiteKey: () => '',
}))

// The stub surfaces the action the form binds, so this test pins this page's
// half of the action contract (the hub siteverify check enforces the same
// string server-side).
vi.mock('~/components/common/CaptchaField', () => ({
  CaptchaField: (props: { action: string }) => <div data-testid="captcha-field" data-action={props.action} />,
}))

const mockSetAuth = vi.fn()
// The sign-in recovery the gate renders; a test sets it when the adoption of
// the completion's report recovers.
const [signInRecovery, setSignInRecovery] = createSignal<SignInRecovery | null>(null)
vi.mock('~/context/AuthContext', () => ({
  useAuth: () => ({
    user: () => null,
    loading: () => false,
    error: () => null,
    login: vi.fn(),
    logout: vi.fn(),
    setAuth: mockSetAuth,
    signInRecovery: () => signInRecovery(),
    retrySignInRecovery: vi.fn(),
    finishSignInRecovery: () => setSignInRecovery(null),
    isAuthenticated: () => false,
  }),
  AuthProvider: (props: { children: unknown }) => <>{props.children}</>,
}))

function createHistoryAt(url: string): MemoryHistory {
  const history = createMemoryHistory()
  history.set({ value: url, scroll: false, replace: true })
  return history
}

function renderPage(token = 'test-token') {
  const history = createHistoryAt(`/?token=${token}`)
  return {
    history,
    ...render(() => (
      <MemoryRouter history={history} root={props => <SignInRecoveryGate>{props.children}</SignInRecoveryGate>}>
        <Route path="/" component={OAuthCompleteSignupPage} />
        <Route path="/verify-email" component={() => <div data-testid="verify-email-page" />} />
      </MemoryRouter>
    )),
  }
}

/** Submit the form as `username` once the pending sign-up loaded. */
async function submitAs(username: string) {
  fireEvent.input(await screen.findByLabelText('Username'), {
    target: { value: username },
  })
  fireEvent.click(screen.getByRole('button', { name: 'Create account' }))
}

describe('signup completion page (OAuthCompleteSignupPage)', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    setSignInRecovery(null)
    mockSetAuth.mockResolvedValue('adopted')
    // Restated, not merely cleared: `clearAllMocks` keeps an implementation
    // that an earlier test set.
    mockIsSystemInfoLoaded.mockReturnValue(true)
  })

  it('renders form with pre-filled display name and email', async () => {
    mockGetPendingOAuthSignup.mockResolvedValue({
      email: 'test@example.com',
      displayName: 'Test User',
      providerName: 'GitHub',
    })

    renderPage()

    await vi.waitFor(() => {
      expect(screen.getByLabelText('Display Name')).toBeInTheDocument()
    })

    const displayNameInput = screen.getByLabelText('Display Name') as HTMLInputElement
    const emailInput = screen.getByLabelText('Email') as HTMLInputElement
    const usernameInput = screen.getByLabelText('Username') as HTMLInputElement

    expect(displayNameInput.value).toBe('Test User')
    expect(emailInput.value).toBe('test@example.com')
    expect(usernameInput.value).toBe('')
    expect(screen.getByText(/Signed in via/)).toBeInTheDocument()
    expect(screen.getByText(/GitHub/)).toBeInTheDocument()
  })

  it('shows error when token is invalid', async () => {
    mockGetPendingOAuthSignup.mockRejectedValue(new Error('invalid token'))

    renderPage()

    await vi.waitFor(() => {
      expect(screen.getByText('This signup link is invalid or expired.')).toBeInTheDocument()
    })
  })

  it('calls completeOAuthSignup on submit', async () => {
    mockGetPendingOAuthSignup.mockResolvedValue({
      email: 'test@example.com',
      displayName: 'Test User',
      providerName: 'GitHub',
    })
    mockCompleteOAuthSignup.mockResolvedValue({
      sessionReport: {
        user: { id: 'u1', username: 'testuser' },
      },
    })

    renderPage()

    await vi.waitFor(() => {
      expect(screen.getByLabelText('Username')).toBeInTheDocument()
    })

    const usernameInput = screen.getByLabelText('Username') as HTMLInputElement
    fireEvent.input(usernameInput, {
      target: { value: 'testuser' },
    })

    const submitButton = screen.getByRole('button', { name: 'Create account' })
    fireEvent.click(submitButton)

    await vi.waitFor(() => {
      expect(mockCompleteOAuthSignup).toHaveBeenCalledWith({
        signupToken: 'test-token',
        username: 'testuser',
        displayName: 'Test User',
        // The hub reads this only when the provider supplied nothing; a
        // provider-supplied address always takes precedence, so echoing it
        // back is safe.
        email: 'test@example.com',
        captcha: { payload: '', honeypot: '' },
      })
    })

    // The pending sign-up carried no redirect, so the adoption gets none.
    expect(mockSetAuth).toHaveBeenCalledWith({
      user: { id: 'u1', username: 'testuser' },
    }, { redirect: undefined })
  })

  // The hub accepted the single-use signup token, so the account exists, but
  // the reply could not report it and the account read fails. The page must
  // never send the token again: the adoption recovers, the recovery panel
  // replaces the page, and Submit is gone -- whatever verification needs.
  for (const verificationRequired of [false, true]) {
    it(`recovers rather than re-submitting when the account cannot be read (verification required: ${verificationRequired})`, async () => {
      mockGetPendingOAuthSignup.mockResolvedValue({
        email: 'test@example.com',
        displayName: 'Test User',
        providerName: 'GitHub',
      })
      mockCompleteOAuthSignup.mockResolvedValue({
        sessionReport: {
          emailVerification: { verificationRequired },
        },
      })
      mockSetAuth.mockImplementation(async () => {
        setSignInRecovery({
          read: { kind: 'failed', message: 'account read failed' },
          verificationRequired,
        })
        return 'recovering'
      })

      renderPage()
      fireEvent.input(await screen.findByLabelText('Username'), {
        target: { value: 'testuser' },
      })
      fireEvent.click(screen.getByRole('button', { name: 'Create account' }))

      expect(await screen.findByTestId('signin-recovery')).toHaveTextContent('account read failed')
      expect(screen.queryByRole('button', { name: 'Create account' })).not.toBeInTheDocument()
      expect(mockCompleteOAuthSignup).toHaveBeenCalledOnce()
      expect(mockSetAuth).toHaveBeenCalledWith({
        emailVerification: { verificationRequired },
      }, { redirect: undefined })
    })
  }

  // Only an adopted account is routed from here. Verification decides the
  // destination, read from the report the completion returned.
  it('goes to the verification page when the adopted account must verify its email', async () => {
    mockGetPendingOAuthSignup.mockResolvedValue({
      email: 'test@example.com',
      displayName: 'Test User',
      providerName: 'GitHub',
    })
    mockCompleteOAuthSignup.mockResolvedValue({
      sessionReport: {
        user: { id: 'u1', username: 'testuser' },
        emailVerification: { verificationRequired: true },
      },
    })

    const { history } = renderPage()
    await submitAs('testuser')

    expect(await screen.findByTestId('verify-email-page')).toBeInTheDocument()
    expect(history.get()).toBe('/verify-email')
  })

  // A CLI login bounces through the sign-in with a consent address that the
  // hub's mux serves. A first-time OAuth sign-up continues here, so the
  // address must survive the sign-up, or the CLI waits for a consent screen
  // that the user never sees.
  it('continues to the address the sign-in asked to reach when the account needs no verification', async () => {
    const consent = '/oauth/authorize?client_id=cli'
    mockGetPendingOAuthSignup.mockResolvedValue({
      email: 'test@example.com',
      displayName: 'Test User',
      providerName: 'GitHub',
      redirect: consent,
    })
    mockCompleteOAuthSignup.mockResolvedValue({
      sessionReport: {
        user: { id: 'u1', username: 'testuser' },
      },
    })
    const assign = vi.fn()
    vi.stubGlobal('location', { assign })
    try {
      const { history } = renderPage()
      await submitAs('testuser')

      // The hub serves the consent page, so a client-side transition would
      // render the SPA's 404 page. The page loads it as a full document.
      await vi.waitFor(() => expect(assign).toHaveBeenCalledExactlyOnceWith(consent))
      expect(history.get()).toBe('/?token=test-token')
      expect(mockSetAuth).toHaveBeenCalledExactlyOnceWith({
        user: { id: 'u1', username: 'testuser' },
      }, { redirect: consent })
    }
    finally {
      vi.unstubAllGlobals()
    }
  })

  // Verification comes first: the account verifies its address before it
  // continues to the consent page.
  it('goes to the verification page over the redirect when the account must verify its email', async () => {
    mockGetPendingOAuthSignup.mockResolvedValue({
      email: 'test@example.com',
      displayName: 'Test User',
      providerName: 'GitHub',
      redirect: '/oauth/authorize?client_id=cli',
    })
    mockCompleteOAuthSignup.mockResolvedValue({
      sessionReport: {
        user: { id: 'u1', username: 'testuser' },
        emailVerification: { verificationRequired: true },
      },
    })
    const assign = vi.fn()
    vi.stubGlobal('location', { assign })
    try {
      const { history } = renderPage()
      await submitAs('testuser')

      expect(await screen.findByTestId('verify-email-page')).toBeInTheDocument()
      expect(history.get()).toBe('/verify-email')
      expect(assign).not.toHaveBeenCalled()
    }
    finally {
      vi.unstubAllGlobals()
    }
  })

  // The hub sanitized the address when the sign-in started, and the page
  // filters it again before it follows it. An address that leaves the origin
  // falls back to the app.
  it('goes home when the redirect would leave the origin', async () => {
    mockGetPendingOAuthSignup.mockResolvedValue({
      email: 'test@example.com',
      displayName: 'Test User',
      providerName: 'GitHub',
      redirect: '//evil.example/steal',
    })
    mockCompleteOAuthSignup.mockResolvedValue({
      sessionReport: {
        user: { id: 'u1', username: 'testuser' },
      },
    })
    const assign = vi.fn()
    vi.stubGlobal('location', { assign })
    try {
      const { history } = renderPage()
      await submitAs('testuser')

      await vi.waitFor(() => expect(history.get()).toBe('/'))
      expect(assign).not.toHaveBeenCalled()
    }
    finally {
      vi.unstubAllGlobals()
    }
  })

  it('goes home when the adopted account needs no verification', async () => {
    mockGetPendingOAuthSignup.mockResolvedValue({
      email: 'test@example.com',
      displayName: 'Test User',
      providerName: 'GitHub',
    })
    mockCompleteOAuthSignup.mockResolvedValue({
      sessionReport: {
        user: { id: 'u1', username: 'testuser' },
      },
    })

    const { history } = renderPage()
    await submitAs('testuser')

    await vi.waitFor(() => expect(history.get()).toBe('/'))
  })

  // A newer sign-in owns the state, so this page must not navigate over it.
  it('stays put when a newer sign-in superseded the adoption', async () => {
    mockGetPendingOAuthSignup.mockResolvedValue({
      email: 'test@example.com',
      displayName: 'Test User',
      providerName: 'GitHub',
    })
    mockCompleteOAuthSignup.mockResolvedValue({
      sessionReport: {
        user: { id: 'u1', username: 'testuser' },
        emailVerification: { verificationRequired: true },
      },
    })
    mockSetAuth.mockResolvedValue('superseded')

    const { history } = renderPage()
    await submitAs('testuser')

    await vi.waitFor(() => expect(mockSetAuth).toHaveBeenCalledOnce())
    await settle()
    expect(history.get()).toBe('/?token=test-token')
    expect(mockCompleteOAuthSignup).toHaveBeenCalledOnce()
  })

  it('shows email as read-only when provider supplies it', async () => {
    mockGetPendingOAuthSignup.mockResolvedValue({
      email: 'provider@example.com',
      displayName: 'Test User',
      providerName: 'GitHub',
    })

    renderPage()

    await vi.waitFor(() => {
      expect(screen.getByLabelText('Email')).toBeInTheDocument()
    })

    const emailInput = screen.getByLabelText('Email') as HTMLInputElement
    expect(emailInput.value).toBe('provider@example.com')
    expect(emailInput.readOnly).toBe(true)
  })

  // An untrusted provider that omits the email claim used to leave the
  // sign-up unrecoverable whenever SMTP was on: the hub refuses an empty
  // address and no step in the flow could produce one, so the pending
  // token expired and the account could never be created.
  it('lets the user supply an email when the provider gave none', async () => {
    mockIsEmailEnabled.mockReturnValue(true)
    mockGetPendingOAuthSignup.mockResolvedValue({
      email: '',
      displayName: 'Test User',
      providerName: 'GitHub',
    })
    mockCompleteOAuthSignup.mockResolvedValue({
      sessionReport: {
        user: { id: 'u1', username: 'testuser' },
      },
    })

    renderPage()

    await vi.waitFor(() => {
      expect(screen.getByLabelText('Email')).toBeInTheDocument()
    })
    const emailInput = screen.getByLabelText('Email') as HTMLInputElement
    expect(emailInput.readOnly).toBe(false)
    expect(emailInput.required).toBe(true)

    fireEvent.input(screen.getByLabelText('Username'), {
      target: { value: 'testuser' },
    })
    fireEvent.input(emailInput, {
      target: { value: '  typed@example.com  ' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Create account' }))

    await vi.waitFor(() => {
      expect(mockCompleteOAuthSignup).toHaveBeenCalledWith(
        expect.objectContaining({ email: 'typed@example.com' }),
      )
    })
    mockIsEmailEnabled.mockReturnValue(false)
  })

  it('shows error for username taken', async () => {
    mockGetPendingOAuthSignup.mockResolvedValue({
      email: 'test@example.com',
      displayName: 'Test User',
      providerName: 'GitHub',
    })
    mockCompleteOAuthSignup.mockRejectedValue(new Error('username already taken'))

    renderPage()

    await vi.waitFor(() => {
      expect(screen.getByLabelText('Username')).toBeInTheDocument()
    })

    const usernameInput = screen.getByLabelText('Username') as HTMLInputElement
    fireEvent.input(usernameInput, {
      target: { value: 'taken-user' },
    })

    const submitButton = screen.getByRole('button', { name: 'Create account' })
    fireEvent.click(submitButton)

    await vi.waitFor(() => {
      expect(screen.getByText('username already taken')).toBeInTheDocument()
    })
    // A failed completion is the one failure that re-enables Submit, and it
    // adopts nothing.
    expect(screen.getByRole('button', { name: 'Create account' })).toBeEnabled()
    expect(mockSetAuth).not.toHaveBeenCalled()
  })

  // A password manager can submit the form itself, past the disabled button.
  // While the system info is pending, the form cannot tell whether the hub
  // requires a captcha, so a completion sent then can arrive without a
  // captcha that the hub requires.
  it('refuses a programmatic submit while the system info is pending', async () => {
    mockGetPendingOAuthSignup.mockResolvedValue({
      email: 'test@example.com',
      displayName: 'Test User',
      providerName: 'GitHub',
    })
    mockIsSystemInfoLoaded.mockReturnValue(false)
    renderPage()
    fireEvent.input(await screen.findByLabelText('Username'), {
      target: { value: 'testuser' },
    })

    screen.getByRole('button', { name: 'Create account' }).closest('form')!.requestSubmit()
    await settle()

    expect(mockCompleteOAuthSignup).not.toHaveBeenCalled()
    expect(screen.getByRole('button', { name: 'Create account' })).toBeDisabled()
  })

  // The hub accepted the single-use token, and the adoption is still out. A
  // second programmatic submit must not send the token again.
  it('refuses a programmatic submit after the hub accepted the token', async () => {
    mockGetPendingOAuthSignup.mockResolvedValue({
      email: 'test@example.com',
      displayName: 'Test User',
      providerName: 'GitHub',
    })
    mockCompleteOAuthSignup.mockResolvedValue({
      sessionReport: {
        user: { id: 'u1', username: 'testuser' },
      },
    })
    mockSetAuth.mockReturnValue(new Promise<never>(() => {}))
    renderPage()
    fireEvent.input(await screen.findByLabelText('Username'), {
      target: { value: 'testuser' },
    })
    const form = screen.getByRole('button', { name: 'Create account' }).closest('form')!

    form.requestSubmit()
    await vi.waitFor(() => expect(mockSetAuth).toHaveBeenCalledOnce())
    form.requestSubmit()
    await settle()

    expect(mockCompleteOAuthSignup).toHaveBeenCalledOnce()
  })

  it('hands the captcha field the complete_signup action under an external provider', async () => {
    mockGetPendingOAuthSignup.mockResolvedValue({
      email: 'test@example.com',
      displayName: 'Test User',
      providerName: 'GitHub',
    })
    mockIsCaptchaEnabled.mockReturnValue(true)
    mockGetCaptchaProvider.mockReturnValue(3) // CaptchaProvider.TURNSTILE

    renderPage()

    await vi.waitFor(() => {
      expect(screen.getByTestId('captcha-field')).toBeInTheDocument()
    })
    // The hub refuses tokens minted under any other action, so this
    // literal is half of a wire contract (the backend half lives in
    // captcha.procedureActions).
    expect(screen.getByTestId('captcha-field')).toHaveAttribute('data-action', 'complete_signup')
  })
})
