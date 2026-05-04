/// <reference types="vitest/globals" />
import { createMemoryHistory, MemoryRouter, Route, useSearchParams } from '@solidjs/router'
import { fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { VerifyEmailPage } from './VerifyEmailPage'

// Mocks ----------------------------------------------------------------

// connect-rpc surfaces server-side connect.NewError(...) as an Error
// whose message starts with "<code_text>:". Our spec doesn't import the
// SDK's ConnectError directly because that would require a heavier
// fixture setup; reproducing the message-shape is sufficient for the
// banner-rendering assertions.
class FakeConnectError extends Error {
  constructor(code: string, msg: string) {
    super(`[${code}] ${msg}`)
  }
}

const mockVerify = vi.fn<(args: { verificationToken: string }) => Promise<{ user?: { username: string, emailVerified: boolean } | null }>>()
const mockResend = vi.fn<(args: Record<string, never>) => Promise<{ emailSent: boolean }>>()

vi.mock('~/api/clients', () => ({
  userClient: {
    verifyEmail: (...a: Parameters<typeof mockVerify>) => mockVerify(...a),
    resendVerificationEmail: (...a: Parameters<typeof mockResend>) => mockResend(...a),
  },
}))

const mockSetAuth = vi.fn()
const mockUser = vi.fn<() => { username: string, emailVerified: boolean } | null>()
vi.mock('~/context/AuthContext', () => ({
  useAuth: () => ({
    user: () => mockUser(),
    loading: () => false,
    error: () => null,
    login: vi.fn(),
    logout: vi.fn(),
    setAuth: mockSetAuth,
    isAuthenticated: () => mockUser() != null,
  }),
  AuthProvider: (props: { children: unknown }) => <>{props.children}</>,
}))

// Helpers --------------------------------------------------------------

// The login mock surfaces its search params via a data attribute so the
// redirect test can assert that the original /verify-email?code=... URL
// is preserved verbatim through the round-trip.
function LoginMock() {
  const [params] = useSearchParams()
  return <div data-testid="login-page" data-redirect={String(params.redirect ?? '')} />
}

function renderPage(initialPath: string) {
  // The router reads its starting URL from history.get() on init, so
  // seeding the entry before render is enough — no listener race.
  const history = createMemoryHistory()
  history.set({ value: initialPath, replace: true, scroll: false })
  return render(() => (
    <MemoryRouter history={history}>
      <Route path="/verify-email" component={VerifyEmailPage} />
      <Route path="/login" component={LoginMock} />
      <Route path="/o/:slug" component={props => <div data-testid="org-page">{props.params.slug}</div>} />
    </MemoryRouter>
  ))
}

beforeEach(() => {
  vi.clearAllMocks()
  mockUser.mockReturnValue({ username: 'alice', emailVerified: false })
})

afterEach(() => {
  vi.useRealTimers()
})

// Tests ----------------------------------------------------------------

describe('verifyEmailPage', () => {
  it('redirects unauthenticated users to /login with the original code preserved in ?redirect=', async () => {
    mockUser.mockReturnValue(null)

    renderPage('/verify-email?code=AB2-CDE')

    // Without a session the page must navigate away — we confirm by
    // landing on the /login route. Preserving the code in `redirect`
    // means the verification can resume after sign-in without the user
    // having to click the email link again. (Use the no-ambiguous
    // charset for the code so the surrounding tests are consistent.)
    const loginPage = await screen.findByTestId('login-page')
    expect(loginPage.getAttribute('data-redirect')).toBe('/verify-email?code=AB2-CDE')
  })

  it('auto-submits when ?code= is present and the user is signed in', async () => {
    mockVerify.mockResolvedValueOnce({
      user: { username: 'alice', emailVerified: true },
    })

    renderPage('/verify-email?code=AB2-CDE')

    await waitFor(() => {
      expect(mockVerify).toHaveBeenCalledOnce()
    })
    // The submitted token must be the *normalized* form (no hyphen,
    // uppercase). Auto-submit uses whatever was in the URL.
    expect(mockVerify).toHaveBeenCalledWith({ verificationToken: 'AB2CDE' })
  })

  it('manually-typed codes accept hyphen and lowercase, then normalize', async () => {
    mockVerify.mockResolvedValueOnce({
      user: { username: 'alice', emailVerified: true },
    })

    renderPage('/verify-email')

    const input = await screen.findByTestId('verify-email-code-input') as HTMLInputElement
    fireEvent.input(input, { target: { value: '7xc-8dz' } })
    fireEvent.click(screen.getByTestId('verify-email-submit'))

    await waitFor(() => {
      expect(mockVerify).toHaveBeenCalledWith({ verificationToken: '7XC8DZ' })
    })
  })

  it('forwards malformed codes to the backend and surfaces its error', async () => {
    // Charset validation is intentionally backend-only — duplicating
    // the alphabet on the FE would mean two places to update. The page
    // strips noise (whitespace/hyphens, uppercases) and lets the server
    // be the source of truth for what's valid.
    mockVerify.mockRejectedValueOnce(new FakeConnectError('invalid_argument', 'invalid verification code'))

    renderPage('/verify-email')

    const input = await screen.findByTestId('verify-email-code-input') as HTMLInputElement
    fireEvent.input(input, { target: { value: 'O0O0O0' } })
    fireEvent.click(screen.getByTestId('verify-email-submit'))

    await waitFor(() => {
      // Submitted *as typed* (sans hyphens), uppercased — backend rejects.
      expect(mockVerify).toHaveBeenCalledWith({ verificationToken: 'O0O0O0' })
    })
    expect(await screen.findByText(/invalid verification code/i)).toBeInTheDocument()
  })

  it('surfaces the server error when the code is rejected', async () => {
    mockVerify.mockRejectedValueOnce(new FakeConnectError('not_found', 'invalid or expired verification code'))

    renderPage('/verify-email')

    const input = await screen.findByTestId('verify-email-code-input') as HTMLInputElement
    fireEvent.input(input, { target: { value: '7XC-8DZ' } })
    fireEvent.click(screen.getByTestId('verify-email-submit'))

    await waitFor(() => {
      // The error banner should reflect the server message verbatim
      // (exact wording is the server's choice; we just need to ensure
      // it's shown rather than swallowed).
      expect(screen.getByText(/invalid or expired/i)).toBeInTheDocument()
    })
  })

  it('clicking Resend issues the RPC and shows a status line on success', async () => {
    mockResend.mockResolvedValueOnce({ emailSent: true })

    renderPage('/verify-email')

    fireEvent.click(await screen.findByTestId('verify-email-resend'))

    await waitFor(() => {
      expect(mockResend).toHaveBeenCalledOnce()
    })
    expect(await screen.findByTestId('verify-email-resend-status'))
      .toHaveTextContent(/A fresh code has been sent/)
  })

  it('resend reports the partial-failure state when the mail send fails', async () => {
    mockResend.mockResolvedValueOnce({ emailSent: false })

    renderPage('/verify-email')

    fireEvent.click(await screen.findByTestId('verify-email-resend'))

    await waitFor(() => {
      expect(mockResend).toHaveBeenCalledOnce()
    })
    expect(await screen.findByTestId('verify-email-resend-status'))
      .toHaveTextContent(/couldn't send/)
  })
})
