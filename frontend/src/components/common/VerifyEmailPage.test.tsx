/// <reference types="vitest/globals" />
import { createMemoryHistory, MemoryRouter, Route, useSearchParams } from '@solidjs/router'
import { fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { CaptchaProvider } from '~/generated/proto/leapmux/v1/auth_pb'
import { setMockCaptchaPayload } from '~/test-support/captchaMocks'
import { resetSystemInfoMock, setSystemInfoMock } from '~/test-support/systemInfoMock'

import { VerifyEmailPage } from './VerifyEmailPage'

// Mocks ----------------------------------------------------------------

// connect-rpc surfaces server-side connect.NewError(...) as an Error
// whose message starts with "<code_text>:". This spec does not import the
// SDK's ConnectError directly, because that needs a heavier fixture
// setup; a reproduction of the message shape is sufficient for the
// banner-rendering assertions.
class FakeConnectError extends Error {
  constructor(code: string, msg: string) {
    super(`[${code}] ${msg}`)
  }
}

const mockVerify = vi.fn<(args: { verificationToken: string } & Record<string, string>) => Promise<{ user?: { username: string, emailVerified: boolean } | null }>>()
const mockResend = vi.fn<(args: { captchaPayload: string, honeypot: string }) => Promise<{ emailSent: boolean }>>()

vi.mock('~/api/clients', () => ({
  userClient: {
    verifyEmail: (...a: Parameters<typeof mockVerify>) => mockVerify(...a),
    resendVerificationEmail: (...a: Parameters<typeof mockResend>) => mockResend(...a),
  },
}))

// The page carries two captcha forms (verify + resend); the default mock
// answers with a loaded snapshot and captcha disabled.
vi.mock('~/lib/systemInfo', async () => {
  const m = await import('~/test-support/systemInfoMock')
  return m.systemInfoMock
})

vi.mock('~/components/common/CaptchaField', async () => (await import('~/test-support/captchaMocks')).captchaFieldMock)
vi.mock('~/components/common/CaptchaHoneypot', async () => (await import('~/test-support/captchaMocks')).captchaHoneypotMock)

const mockSetAuth = vi.fn()
// Verifying an address is a REFRESH of the same identity, not a transition to
// a new one, so the page adopts the response through adoptSameIdentityUser
// rather than setAuth -- which clears the session's elevation deadline the hub
// never touched.
const mockAdoptSameIdentityUser = vi.fn()
const mockRefreshUser = vi.fn<() => Promise<void>>(async () => {})
const mockUser = vi.fn<() => { username: string, emailVerified: boolean } | null>()
// A signal, not a vi.fn: the cooldownKnown gate re-runs when loading flips,
// so a test can simulate the auth bootstrap answering mid-flight.
const [authLoading, setAuthLoading] = createSignal(false)
const mockVerificationResendAvailableAt = vi.fn<() => { seconds: bigint, nanos: number } | undefined>()
vi.mock('~/context/AuthContext', () => ({
  useAuth: () => ({
    user: () => mockUser(),
    loading: () => authLoading(),
    error: () => null,
    login: vi.fn(),
    logout: vi.fn(),
    setAuth: mockSetAuth,
    adoptSameIdentityUser: mockAdoptSameIdentityUser,
    refreshUser: mockRefreshUser,
    verificationResendAvailableAt: () => mockVerificationResendAvailableAt(),
    setVerificationResendAvailableAt: vi.fn(),
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
      <Route path="/" component={() => <div data-testid="app-home" />} />
    </MemoryRouter>
  ))
}

beforeEach(() => {
  vi.clearAllMocks()
  resetSystemInfoMock()
  setMockCaptchaPayload(null)
  mockUser.mockReturnValue({ username: 'alice', emailVerified: false })
})

afterEach(() => {
  vi.useRealTimers()
})

// Tests ----------------------------------------------------------------

describe('verifyEmailPage', () => {
  it('passes the verify_email and resend_verification actions to their captcha fields', async () => {
    // Both paths are captcha-protected: the verify path burns a guess for
    // free, and the resend path drives an SMTP send. Each renders its own
    // field under its own action, in that order.
    setSystemInfoMock({ captchaEnabled: true, captchaProvider: CaptchaProvider.TURNSTILE, captchaSiteKey: '1x00000000000000000000AA' })
    renderPage('/verify-email')

    await vi.waitFor(() => {
      expect(screen.getAllByTestId('captcha-field').map(el => el.getAttribute('data-action')))
        .toEqual(['verify_email', 'resend_verification'])
    })
  })

  it('redirects unauthenticated users to /login with the original code preserved in ?redirect=', async () => {
    mockUser.mockReturnValue(null)

    renderPage('/verify-email?code=AB2-CDE')

    // Without a session the page must navigate away — this test confirms
    // it by landing on the /login route. Preserving the code in `redirect`
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
    expect(mockVerify).toHaveBeenCalledWith(expect.objectContaining({ verificationToken: 'AB2CDE' }))
  })

  // The emailed link's auto-submit WAITS for the captcha instead of failing
  // one-shot. The shape this replaces burned its single attempt on "The
  // captcha is still solving" the moment the page mounted on a captcha-
  // enabled hub, and left the user to press Verify by hand.
  it('auto-submits the emailed code once the captcha payload arrives', async () => {
    setSystemInfoMock({ captchaEnabled: true, captchaProvider: CaptchaProvider.TURNSTILE, captchaSiteKey: '1x00000000000000000000AA' })
    mockVerify.mockResolvedValueOnce({
      user: { username: 'alice', emailVerified: true },
    })

    renderPage('/verify-email?code=AB2-CDE')

    // No payload yet: both fields render, nothing submits, and the page
    // shows no error about a captcha that has not answered.
    await waitFor(() => {
      expect(screen.getAllByTestId('captcha-field')).toHaveLength(2)
    })
    expect(mockVerify).not.toHaveBeenCalled()
    expect(screen.queryByText(/still solving/i)).not.toBeInTheDocument()

    // The widget delivers its payload; the queued code submits itself.
    setMockCaptchaPayload('turnstile-token')
    await waitFor(() => {
      expect(mockVerify).toHaveBeenCalledWith(expect.objectContaining({ verificationToken: 'AB2CDE' }))
    })
    expect(await screen.findByTestId('app-home')).toBeInTheDocument()
  })

  it('redirects to / after a successful verification', async () => {
    mockVerify.mockResolvedValueOnce({
      user: { username: 'alice', emailVerified: true },
    })

    renderPage('/verify-email?code=AB2-CDE')

    // The emailed link lands here; on success the page must hand the user off
    // to the flat user-owned home (`/`) rather than leaving them on the form.
    // Same landing the post-login redirect asserts in LoginPage.test.tsx.
    expect(await screen.findByTestId('app-home')).toBeInTheDocument()
  })

  // The RESPONSE is the authoritative account. The page used to discard it and
  // re-read through refreshUser, whose only failure path is an empty catch, and
  // then navigate home regardless: a blip on that second round trip left
  // `emailVerified` false for an address that the hub just verified, so
  // Preferences still rendered "unverified / Verify" and RegisterWorkerDialog
  // kept its email control disabled.
  it('adopts the verified account from the response itself', async () => {
    const verified = { username: 'alice', emailVerified: true }
    mockVerify.mockResolvedValueOnce({ user: verified })

    renderPage('/verify-email?code=AB2-CDE')

    await waitFor(() => {
      expect(mockAdoptSameIdentityUser).toHaveBeenCalledWith(verified)
    })
    // adoptSameIdentityUser, never setAuth: the identity did not change, and
    // setAuth clears an elevation window the hub never touched.
    expect(mockSetAuth).not.toHaveBeenCalled()
  })

  it('still lands home when the follow-up refresh fails', async () => {
    const verified = { username: 'alice', emailVerified: true }
    mockVerify.mockResolvedValueOnce({ user: verified })
    // refreshUser discards its own failure, which is why the response has to
    // carry the account: the page navigates home either way.
    mockRefreshUser.mockImplementationOnce(async () => {})

    renderPage('/verify-email?code=AB2-CDE')

    expect(await screen.findByTestId('app-home')).toBeInTheDocument()
    expect(mockAdoptSameIdentityUser).toHaveBeenCalledWith(verified)
  })

  // The refresh STAYS: the response carries the user and nothing else, while
  // the resend cooldown and the elevation deadline come from GetCurrentUser.
  it('keeps the account re-read for the signals the response does not carry', async () => {
    mockVerify.mockResolvedValueOnce({ user: { username: 'alice', emailVerified: true } })

    renderPage('/verify-email?code=AB2-CDE')

    await waitFor(() => {
      expect(mockRefreshUser).toHaveBeenCalledTimes(1)
    })
  })

  // A hub that answers without one leaves the cached account alone rather than
  // adopting a null user, which would sign the browser out of a session the
  // verification did not touch.
  it('adopts nothing when the response carries no user', async () => {
    mockVerify.mockResolvedValueOnce({})

    renderPage('/verify-email?code=AB2-CDE')

    expect(await screen.findByTestId('app-home')).toBeInTheDocument()
    expect(mockAdoptSameIdentityUser).not.toHaveBeenCalled()
    expect(mockRefreshUser).toHaveBeenCalledTimes(1)
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
      expect(mockVerify).toHaveBeenCalledWith(expect.objectContaining({ verificationToken: '7XC8DZ' }))
    })
  })

  it('forwards malformed codes to the backend and surfaces its error', async () => {
    // Charset validation is intentionally backend-only — a copy of the
    // alphabet in the frontend would mean two places to update. The page
    // strips the formatting characters (whitespace/hyphens), uppercases the
    // rest, and lets the server be the source of truth for what is valid.
    mockVerify.mockRejectedValueOnce(new FakeConnectError('invalid_argument', 'invalid verification code'))

    renderPage('/verify-email')

    const input = await screen.findByTestId('verify-email-code-input') as HTMLInputElement
    fireEvent.input(input, { target: { value: 'O0O0O0' } })
    fireEvent.click(screen.getByTestId('verify-email-submit'))

    await waitFor(() => {
      // Submitted *as typed* (without hyphens), uppercased — backend rejects.
      expect(mockVerify).toHaveBeenCalledWith(expect.objectContaining({ verificationToken: 'O0O0O0' }))
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
      // (the exact wording is the server's choice; this test only confirms
      // that the page shows it rather than discarding it).
      expect(screen.getByText(/invalid or expired/i)).toBeInTheDocument()
    })
  })

  it('seeds resend cooldown from login verification timestamp', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-01-01T00:00:00Z'))
    mockVerificationResendAvailableAt.mockReturnValue({
      seconds: BigInt(Math.floor(Date.now() / 1000) + 42),
      nanos: 0,
    })

    renderPage('/verify-email')

    const resendButton = await screen.findByTestId('verify-email-resend')
    expect(resendButton).toBeDisabled()
    expect(resendButton).toHaveTextContent(/Resend code \(0:42\)/)
  })

  it('clicking Resend issues the RPC and shows a status line on success', async () => {
    mockResend.mockResolvedValueOnce({ emailSent: true })

    renderPage('/verify-email')

    fireEvent.click(await screen.findByTestId('verify-email-resend'))

    await waitFor(() => {
      expect(mockResend).toHaveBeenCalledOnce()
    })
    expect(await screen.findByTestId('verify-email-resend-status'))
      .toHaveTextContent(/sent a fresh code/i)
  })

  // The resend widget arms only when a resend is LEGAL: mounting it during
  // the cooldown minted a challenge that typically expired unused (the page
  // navigates away on verify success), and a reCAPTCHA v3 field re-minted
  // one every 110 seconds while visible.
  it('mounts the resend captcha widget only once the countdown ends', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-01-01T00:00:00Z'))
    setSystemInfoMock({ captchaEnabled: true, captchaProvider: CaptchaProvider.TURNSTILE, captchaSiteKey: '1x00000000000000000000AA' })
    // A fixed seed, captured once: a mock that recomputed from Date.now()
    // would re-seed on every tick and the countdown would never end.
    mockVerificationResendAvailableAt.mockReturnValue({
      seconds: BigInt(Math.floor(Date.now() / 1000) + 42),
      nanos: 0,
    })

    renderPage('/verify-email')

    // During the cooldown: only the verify widget exists.
    await waitFor(() => {
      expect(screen.getAllByTestId('captcha-field').map(el => el.getAttribute('data-action')))
        .toEqual(['verify_email'])
    })

    // Past the countdown: the resend widget arms.
    vi.advanceTimersByTime(43_000)
    await waitFor(() => {
      expect(screen.getAllByTestId('captcha-field').map(el => el.getAttribute('data-action')))
        .toEqual(['verify_email', 'resend_verification'])
    })
  })

  // A countdown of 0 while the auth bootstrap has not answered means "not
  // known yet", not "a resend is legal now": arming the widget on that
  // reading mints a challenge the arriving seed immediately unmounts -- a
  // wasted hub challenge per hard reload, exactly the cost the gate exists
  // to prevent.
  it('holds the resend widget back until the cooldown seed can be known', async () => {
    setSystemInfoMock({ captchaEnabled: true, captchaProvider: CaptchaProvider.TURNSTILE, captchaSiteKey: '1x00000000000000000000AA' })
    setAuthLoading(true)
    mockVerificationResendAvailableAt.mockReturnValue(undefined)

    renderPage('/verify-email')

    // The bootstrap has not answered: only the verify widget exists, even
    // though the countdown reads zero.
    await waitFor(() => {
      expect(screen.getAllByTestId('captcha-field').map(el => el.getAttribute('data-action')))
        .toEqual(['verify_email'])
    })

    // The bootstrap answers with no deadline: a resend is legal now, and
    // the widget arms.
    setAuthLoading(false)
    await waitFor(() => {
      expect(screen.getAllByTestId('captcha-field').map(el => el.getAttribute('data-action')))
        .toEqual(['verify_email', 'resend_verification'])
    })
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

  // A stale "invalid or expired verification code" must not survive a
  // resend: beside "We sent a fresh code to your inbox" it reads as though
  // the new code was rejected too. The control's beforeResend slot is the
  // wiring that clears it.
  it('clears the stale code error when a resend succeeds', async () => {
    mockVerify.mockRejectedValueOnce(new FakeConnectError('not_found', 'invalid or expired verification code'))
    mockResend.mockResolvedValueOnce({ emailSent: true })

    renderPage('/verify-email')

    const input = await screen.findByTestId('verify-email-code-input') as HTMLInputElement
    fireEvent.input(input, { target: { value: '7XC-8DZ' } })
    fireEvent.click(screen.getByTestId('verify-email-submit'))
    expect(await screen.findByText(/invalid or expired/i)).toBeInTheDocument()

    fireEvent.click(screen.getByTestId('verify-email-resend'))
    await waitFor(() => {
      expect(mockResend).toHaveBeenCalledOnce()
    })
    await waitFor(() => {
      expect(screen.queryByText(/invalid or expired/i)).not.toBeInTheDocument()
    })
    expect(await screen.findByTestId('verify-email-resend-status'))
      .toHaveTextContent(/sent a fresh code/i)
  })
})
