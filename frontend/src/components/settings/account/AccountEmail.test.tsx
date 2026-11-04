import { MemoryRouter, Route } from '@solidjs/router'
import { cleanup, fireEvent, render, screen } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { sessionStorageClearForTests, setStorageAccountForTests } from '~/lib/browserStorage'
import { deferred } from '~/test-support/async'
import { resetSystemInfoMock, setSystemInfoMock } from '~/test-support/systemInfoMock'
import { AccountEmail } from './AccountEmail'

const mockRequestEmailChange = vi.fn()
const mockResendVerificationEmail = vi.fn()
const mockUser = vi.fn()
const mockRefreshUser = vi.fn()
// The adoption the surface opens before the write leaves. The order records when
// it opened relative to the RPC.
const callOrder: string[] = []
const mockAdopt = vi.fn<(user: unknown) => Promise<void>>(async () => {})
const mockOpenUserAdoption = vi.fn(() => {
  callOrder.push('open')
  return mockAdopt
})

vi.mock('~/api/clients', () => ({
  userClient: {
    requestEmailChange: (...args: unknown[]) => mockRequestEmailChange(...args),
    resendVerificationEmail: (...args: unknown[]) => mockResendVerificationEmail(...args),
  },
}))

vi.mock('~/context/AuthContext', () => ({
  useAuth: () => ({
    user: () => mockUser(),
    refreshUser: mockRefreshUser,
    openUserAdoption: mockOpenUserAdoption,
    verificationResendAvailableAt: () => undefined,
  }),
}))

vi.mock('~/lib/systemInfo', async () => {
  const m = await import('~/test-support/systemInfoMock')
  return m.systemInfoMock
})

const verified = {
  id: 'user-1',
  username: 'alice',
  displayName: 'Alice',
  email: 'alice@example.com',
  emailVerified: true,
  passwordSet: true,
  passkeyCount: 0,
  oauthProviders: [],
}

const unverified = { ...verified, emailVerified: false }

// The row links to /verify-email, so it needs a router context.
function renderRouted() {
  return render(() => (
    <MemoryRouter>
      <Route path="/" component={AccountEmail} />
    </MemoryRouter>
  ))
}

describe('AccountEmail', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    resetSystemInfoMock()
    mockUser.mockReturnValue(verified)
    mockRefreshUser.mockResolvedValue(undefined)
    mockRequestEmailChange.mockResolvedValue({
      emailVerification: { verificationRequired: false },
    })
    mockResendVerificationEmail.mockResolvedValue({
      emailVerification: { verificationEmailSent: true },
    })
    mockAdopt.mockImplementation(async () => {})
    callOrder.length = 0
  })

  it('requests the change and reports what the hub did', async () => {
    renderRouted()
    fireEvent.input(await screen.findByLabelText('New Email'), {
      target: { value: 'new@example.com' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Change Email' }))

    await vi.waitFor(() => {
      expect(mockRequestEmailChange).toHaveBeenCalledWith({ newEmail: 'new@example.com' })
    })
    expect(await screen.findByText('Email updated.')).toBeInTheDocument()
  })

  // The control must stay disabled until the cached account is current. An
  // unawaited refresh re-enables it while the user still carries the old
  // address and no pending change.
  it('keeps the control disabled until the cached account is current', async () => {
    const { promise, resolve } = deferred<void>()
    mockAdopt.mockReturnValue(promise)

    renderRouted()
    fireEvent.input(await screen.findByLabelText('New Email'), {
      target: { value: 'new@example.com' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Change Email' }))

    await vi.waitFor(() => {
      expect(mockAdopt).toHaveBeenCalledTimes(1)
    })
    expect(screen.getByRole('button', { name: 'Requesting...' })).toBeDisabled()

    resolve()
    await vi.waitFor(() => {
      expect(screen.getByRole('button', { name: 'Change Email' })).toBeInTheDocument()
    })
  })

  // Opened before the request leaves, so a reply that lands after an identity
  // change re-reads the new identity instead of adopting this one.
  it('adopts the stored account through an adoption opened first', async () => {
    const stored = { ...verified, email: 'new@example.com' }
    const noVerification = { verificationRequired: false }
    mockRequestEmailChange.mockImplementation(async () => {
      callOrder.push('request')
      return { emailVerification: noVerification, user: stored }
    })
    renderRouted()
    fireEvent.input(await screen.findByLabelText('New Email'), {
      target: { value: 'new@example.com' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Change Email' }))

    await vi.waitFor(() => expect(mockAdopt).toHaveBeenCalledExactlyOnceWith(stored, noVerification))
    expect(callOrder).toEqual(['open', 'request'])
    expect(await screen.findByText('Email updated.')).toBeInTheDocument()
    expect(mockRefreshUser).not.toHaveBeenCalled()
  })

  // The reply states the cooldown of the code that the hub just minted, so the
  // resend control counts from the hub's deadline with no account read.
  it('hands the adoption the resend cooldown of the code that the hub minted', async () => {
    const stored = { ...verified, pendingEmail: 'new@example.com' }
    const status = { verificationRequired: true, verificationEmailSent: true }
    mockRequestEmailChange.mockResolvedValue({ emailVerification: status, user: stored })
    renderRouted()
    fireEvent.input(await screen.findByLabelText('New Email'), {
      target: { value: 'new@example.com' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Change Email' }))

    await vi.waitFor(() => expect(mockAdopt).toHaveBeenCalledExactlyOnceWith(stored, status))
    expect(await screen.findByText(/Check your inbox/)).toBeInTheDocument()
    expect(mockRefreshUser).not.toHaveBeenCalled()
  })

  // A refused request changed nothing, so nothing is adopted and the draft stays.
  it('adopts nothing when the hub refuses the request', async () => {
    mockRequestEmailChange.mockRejectedValue(new Error('email is already in use'))
    renderRouted()
    fireEvent.input(await screen.findByLabelText('New Email'), {
      target: { value: 'taken@example.com' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Change Email' }))

    expect(await screen.findByText(/already in use/)).toBeInTheDocument()
    expect(mockAdopt).not.toHaveBeenCalled()
    expect(mockRefreshUser).not.toHaveBeenCalled()
  })

  it('says that the hub sent the code when it verifies first', async () => {
    mockRequestEmailChange.mockResolvedValue({
      emailVerification: { verificationEmailSent: true },
    })
    renderRouted()
    fireEvent.input(await screen.findByLabelText('New Email'), {
      target: { value: 'new@example.com' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Change Email' }))

    expect(await screen.findByText(/Check your inbox/)).toBeInTheDocument()
  })

  // Re-submitting the address the account already holds is a round trip the
  // hub answers with "email is unchanged"; saying so here saves it.
  it('refuses the address the account already holds', async () => {
    renderRouted()
    fireEvent.input(await screen.findByLabelText('New Email'), {
      target: { value: 'ALICE@example.com' },
    })

    expect(await screen.findByText('This is already your current email.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Change Email' })).toBeDisabled()
  })

  it('reports a refusal instead of clearing the field', async () => {
    mockRequestEmailChange.mockRejectedValue(new Error('email is already in use'))
    renderRouted()
    fireEvent.input(await screen.findByLabelText('New Email'), {
      target: { value: 'taken@example.com' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Change Email' }))

    expect(await screen.findByText(/already in use/)).toBeInTheDocument()
    expect(screen.getByLabelText('New Email')).toHaveValue('taken@example.com')
  })

  it('marks the current address verified or not', async () => {
    renderRouted()
    expect(await screen.findByText('(verified)')).toBeInTheDocument()

    mockUser.mockReturnValue(unverified)
    // A second render, because `user()` is a plain mock rather than a signal.
    renderRouted()
    expect(await screen.findByText('(unverified)')).toBeInTheDocument()
  })
})

/**
 * An unverified address needs a route to a confirmed one ON THIS ROW, and
 * until this row existed it had none.
 *
 * `verificationStatusFor` short-circuits on IsAdmin, so the app never routes
 * an administrator to /verify-email, and an administrator's "Change Email"
 * writes the new address straight to the column with no code sent. The /setup
 * administrator therefore landed unverified with no route to a confirmed
 * address -- silently disabling account recovery, the worker-instructions mail
 * and the CLI-credential notice for the one account that most needs them, while
 * `docs/using/accounts.md` told the operator to "verify it from Preferences,
 * Account".
 */
// The field survives a full-document round trip, because one account shape is
// sent on one every time it changes its address: an account with no password
// and no passkey elevates only at its identity provider, and that option
// leaves the app. Losing the address there meant retyping it on the one shape
// that has no other way to verify.
describe('AccountEmail draft', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    resetSystemInfoMock()
    mockUser.mockReturnValue(verified)
    mockRefreshUser.mockResolvedValue(undefined)
    mockRequestEmailChange.mockResolvedValue({
      emailVerification: { verificationRequired: false },
    })
    sessionStorageClearForTests()
    setStorageAccountForTests('user-1')
  })

  it('restores the address the user typed before the round trip', async () => {
    renderRouted()
    fireEvent.input(await screen.findByLabelText('New Email'), {
      target: { value: 'new@example.com' },
    })
    // A full-document navigation: everything in memory goes, the store stays.
    cleanup()

    renderRouted()
    expect(await screen.findByLabelText('New Email')).toHaveValue('new@example.com')
  })

  it('keeps nothing once the change is sent', async () => {
    renderRouted()
    fireEvent.input(await screen.findByLabelText('New Email'), {
      target: { value: 'new@example.com' },
    })
    fireEvent.click(screen.getByRole('button', { name: /change email/i }))
    await screen.findByText('Email updated.')
    cleanup()

    renderRouted()
    expect(await screen.findByLabelText('New Email')).toHaveValue('')
  })

  it('keeps nothing once the user clears the field', async () => {
    renderRouted()
    const field = await screen.findByLabelText('New Email')
    fireEvent.input(field, {
      target: { value: 'new@example.com' },
    })
    fireEvent.input(field, {
      target: { value: '' },
    })
    cleanup()

    renderRouted()
    expect(await screen.findByLabelText('New Email')).toHaveValue('')
  })

  // The address is the account's, and the store is shared by every account on
  // the origin. browserStorage scopes it, and this is what proves the scoping
  // is actually reached from here.
  it('does not offer one account the address another typed', async () => {
    renderRouted()
    fireEvent.input(await screen.findByLabelText('New Email'), {
      target: { value: 'new@example.com' },
    })
    cleanup()

    setStorageAccountForTests('user-2')
    renderRouted()
    expect(await screen.findByLabelText('New Email')).toHaveValue('')
  })
})

describe('AccountEmail verification', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    resetSystemInfoMock()
    setSystemInfoMock({ emailEnabled: true })
    mockUser.mockReturnValue(unverified)
    mockRefreshUser.mockResolvedValue(undefined)
    mockResendVerificationEmail.mockResolvedValue({
      emailVerification: { verificationEmailSent: true },
    })
  })

  it('offers a resend for an unverified address', async () => {
    renderRouted()

    fireEvent.click(await screen.findByRole('button', { name: 'Resend code' }))

    await vi.waitFor(() => {
      expect(mockResendVerificationEmail).toHaveBeenCalledWith({
        captcha: { payload: '', honeypot: '' },
      })
    })
    expect(await screen.findByText(/fresh code/i)).toBeInTheDocument()
  })

  it('offers nothing once the address is verified', async () => {
    mockUser.mockReturnValue(verified)
    renderRouted()
    await screen.findByLabelText('New Email')
    expect(screen.queryByRole('button', { name: 'Resend code' })).not.toBeInTheDocument()
  })

  // No SMTP means no code can arrive, so a control that promises one is a dead
  // end rather than a remedy.
  it('offers nothing when the hub cannot send mail', async () => {
    setSystemInfoMock({ emailEnabled: false })
    renderRouted()
    await screen.findByLabelText('New Email')
    expect(screen.queryByRole('button', { name: 'Resend code' })).not.toBeInTheDocument()
  })

  it('specifies the pending address while one is outstanding', async () => {
    mockUser.mockReturnValue({ ...unverified, pendingEmail: 'next@example.com' })
    renderRouted()
    expect(await screen.findByText('next@example.com')).toBeInTheDocument()
  })
})
