import { fireEvent, render, screen } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { mockLoadSystemInfo } from '~/test-support/systemInfoMock'
import { PasswordSetupGate } from './PasswordSetupGate'

const mockSetInitialSoloPassword = vi.fn()
const mockRefreshUser = vi.fn()
const mockSetAuth = vi.fn()

vi.mock('~/api/clients', () => ({
  authClient: { setInitialSoloPassword: (...args: unknown[]) => mockSetInitialSoloPassword(...args) },
}))

vi.mock('~/context/AuthContext', () => ({
  useAuth: () => ({ refreshUser: () => mockRefreshUser(), setAuth: (...args: unknown[]) => mockSetAuth(...args) }),
}))

vi.mock('~/lib/systemInfo', async () => {
  const m = await import('~/test-support/systemInfoMock')
  return m.systemInfoMock
})

/** Types a valid password pair into the gate. */
function fillPassword(pw = 'correct-horse-battery-staple') {
  fireEvent.input(screen.getByLabelText('New Password'), { target: { value: pw } })
  fireEvent.input(screen.getByLabelText('Confirm Password'), { target: { value: pw } })
}

describe('passwordSetupGate', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mockSetInitialSoloPassword.mockResolvedValue({ user: { id: 'solo-id', username: 'solo' } })
    // Restated, not merely cleared: `clearAllMocks` forgets the CALLS and
    // keeps the implementation, so one test's `mockRejectedValue` would
    // otherwise reject in every test after it.
    mockLoadSystemInfo.mockResolvedValue(undefined)
    mockRefreshUser.mockResolvedValue(undefined)
  })

  // A solo hub has exactly one account, named "solo", so a free field could
  // only be filled in with a name that cannot sign in.
  it('pre-fills the username and refuses edits to it', () => {
    render(() => <PasswordSetupGate />)

    const username = screen.getByLabelText('Username')
    expect(username).toHaveValue('solo')
    expect(username).toHaveAttribute('readonly')
  })

  it('refuses to submit until the two passwords agree', () => {
    render(() => <PasswordSetupGate />)
    const submit = screen.getByRole('button', { name: 'Set Password' })
    expect(submit).toBeDisabled()

    fireEvent.input(screen.getByLabelText('New Password'), { target: { value: 'correct-horse-battery-staple' } })
    expect(submit).toBeDisabled()

    fireEvent.input(screen.getByLabelText('Confirm Password'), { target: { value: 'something-else-entirely' } })
    expect(submit).toBeDisabled()
  })

  it('sets the password and re-reads the hub', async () => {
    render(() => <PasswordSetupGate />)
    fillPassword()

    fireEvent.click(screen.getByRole('button', { name: 'Set Password' }))

    await vi.waitFor(() => {
      expect(mockSetInitialSoloPassword).toHaveBeenCalledWith({ password: 'correct-horse-battery-staple' })
      expect(mockSetAuth).toHaveBeenCalledWith({ id: 'solo-id', username: 'solo' })
      // FORCED, because the gate reads exactly this snapshot: without it the
      // screen stays up over a hub that no longer needs it.
      expect(mockLoadSystemInfo).toHaveBeenCalledWith(true)
      // BOTH copies of "does this account hold a password". The snapshot takes
      // this screen down; the ACCOUNT is what Preferences -> Account renders
      // its button and its solo warning from, and nothing else re-reads it for
      // the life of the page. Without this the operator who just set the first
      // password finds a screen offering to set one.
      expect(mockRefreshUser).toHaveBeenCalled()
    })
  })

  it('reports a refusal rather than leaving the form silent', async () => {
    mockSetInitialSoloPassword.mockRejectedValue(new Error('password is too short'))
    render(() => <PasswordSetupGate />)
    fillPassword()

    fireEvent.click(screen.getByRole('button', { name: 'Set Password' }))

    expect(await screen.findByText(/password is too short/)).toBeInTheDocument()
    // The gate stays up: the hub still has no password, so the exposure stands.
    expect(screen.getByRole('button', { name: 'Set Password' })).toBeInTheDocument()
  })

  // The password committed and only the re-read failed. Reporting that as
  // "Failed to set the password" told the operator to retry a write that
  // succeeded, from a screen that offers no other way out.
  it('says the password landed when only the re-read failed', async () => {
    mockLoadSystemInfo.mockRejectedValue(new Error('connection reset'))
    render(() => <PasswordSetupGate />)
    fillPassword()

    fireEvent.click(screen.getByRole('button', { name: 'Set Password' }))

    expect(await screen.findByText(/The password is set/)).toBeInTheDocument()
    expect(screen.queryByText(/Failed to set the password/)).toBeNull()
    expect(mockSetInitialSoloPassword).toHaveBeenCalledTimes(1)
  })

  it('explains the restricted TCP setup state', () => {
    render(() => <PasswordSetupGate />)
    expect(screen.getByTestId('password-setup-gate')).toHaveTextContent(
      'Until then, TCP callers can only complete this setup',
    )
  })
})
