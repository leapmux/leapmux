import { MemoryRouter, Route } from '@solidjs/router'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { resetSystemInfoMock, setSystemInfoMock } from '~/test-support/systemInfoMock'
import { AccountEmail } from './AccountEmail'

const mockRequestEmailChange = vi.fn()
const mockResendVerificationEmail = vi.fn()
const mockUser = vi.fn()
const mockRefreshUser = vi.fn()

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

describe('accountEmail', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    resetSystemInfoMock()
    mockUser.mockReturnValue(verified)
    mockRefreshUser.mockResolvedValue(undefined)
    mockRequestEmailChange.mockResolvedValue({ verificationRequired: false })
    mockResendVerificationEmail.mockResolvedValue({ emailSent: true })
  })

  it('requests the change and reports what the hub did', async () => {
    renderRouted()
    fireEvent.input(await screen.findByLabelText('New Email'), { target: { value: 'new@example.com' } })
    fireEvent.click(screen.getByRole('button', { name: 'Change Email' }))

    await vi.waitFor(() => {
      expect(mockRequestEmailChange).toHaveBeenCalledWith({ newEmail: 'new@example.com' })
    })
    expect(await screen.findByText('Email updated.')).toBeInTheDocument()
  })

  it('says the code is on its way when the hub verifies first', async () => {
    mockRequestEmailChange.mockResolvedValue({ verificationRequired: true })
    renderRouted()
    fireEvent.input(await screen.findByLabelText('New Email'), { target: { value: 'new@example.com' } })
    fireEvent.click(screen.getByRole('button', { name: 'Change Email' }))

    expect(await screen.findByText(/Check your inbox/)).toBeInTheDocument()
  })

  // Re-submitting the address the account already holds is a round trip the
  // hub answers with "email is unchanged"; saying so here saves it.
  it('refuses the address the account already holds', async () => {
    renderRouted()
    fireEvent.input(await screen.findByLabelText('New Email'), { target: { value: 'ALICE@example.com' } })

    expect(await screen.findByText('This is already your current email.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Change Email' })).toBeDisabled()
  })

  it('reports a refusal instead of clearing the field', async () => {
    mockRequestEmailChange.mockRejectedValue(new Error('email is already in use'))
    renderRouted()
    fireEvent.input(await screen.findByLabelText('New Email'), { target: { value: 'taken@example.com' } })
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
 * administrator therefore landed unverified with no way back -- silently
 * disabling Forgot password, the worker-instructions mail and the
 * CLI-credential notice for the one account that most needs them, while
 * `docs/using/accounts.md` told the operator to "verify it from Preferences,
 * Account".
 */
describe('accountEmail verification', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    resetSystemInfoMock()
    setSystemInfoMock({ emailEnabled: true })
    mockUser.mockReturnValue(unverified)
    mockRefreshUser.mockResolvedValue(undefined)
    mockResendVerificationEmail.mockResolvedValue({ emailSent: true })
  })

  it('offers a resend for an unverified address', async () => {
    renderRouted()

    fireEvent.click(await screen.findByRole('button', { name: 'Resend code' }))

    await vi.waitFor(() => {
      expect(mockResendVerificationEmail).toHaveBeenCalledWith({})
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

  it('names the pending address while one is outstanding', async () => {
    mockUser.mockReturnValue({ ...unverified, pendingEmail: 'next@example.com' })
    renderRouted()
    expect(await screen.findByText('next@example.com')).toBeInTheDocument()
  })
})
