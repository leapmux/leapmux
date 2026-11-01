import { fireEvent, render, screen } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { settle } from '~/test-support/async'
import { mockLoadSystemInfo } from '~/test-support/systemInfoMock'
import { PasswordSetupGate } from './PasswordSetupGate'

const mockSetInitialSoloPassword = vi.fn()
const mockRefreshUser = vi.fn()
const mockSetAuth = vi.fn()
// A stand-in for the protobuf Timestamp the hub returns; the gate only passes
// it through, so its shape does not matter here.
const soloElevation = { seconds: 1700000000n, nanos: 0 }

vi.mock('~/api/clients', () => ({
  authClient: {
    setInitialSoloPassword: (...args: unknown[]) => mockSetInitialSoloPassword(...args),
  },
}))

vi.mock('~/context/AuthContext', () => ({
  useAuth: () => ({
    refreshUser: () => mockRefreshUser(),
    setAuth: (...args: unknown[]) => mockSetAuth(...args),
  }),
}))

vi.mock('~/lib/systemInfo', async () => {
  const m = await import('~/test-support/systemInfoMock')
  return m.systemInfoMock
})

/** Types a valid password pair into the gate. */
function fillPassword(pw = 'correct-horse-battery-staple') {
  fireEvent.input(screen.getByLabelText('New Password'), {
    target: { value: pw },
  })
  fireEvent.input(screen.getByLabelText('Confirm Password'), {
    target: { value: pw },
  })
}

describe('PasswordSetupGate', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mockSetInitialSoloPassword.mockResolvedValue({
      sessionReport: {
        user: { id: 'solo-id', username: 'solo' },
        elevationExpiresAt: soloElevation,
      },
    })
    mockSetAuth.mockResolvedValue('adopted')
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

    fireEvent.input(screen.getByLabelText('New Password'), {
      target: { value: 'correct-horse-battery-staple' },
    })
    expect(submit).toBeDisabled()

    fireEvent.input(screen.getByLabelText('Confirm Password'), {
      target: { value: 'something-else-entirely' },
    })
    expect(submit).toBeDisabled()
  })

  it('sets the password and re-reads the hub', async () => {
    render(() => <PasswordSetupGate />)
    fillPassword()

    fireEvent.click(screen.getByRole('button', { name: 'Set Password' }))

    await vi.waitFor(() => {
      expect(mockSetInitialSoloPassword).toHaveBeenCalledWith({ password: 'correct-horse-battery-staple' })
      // The elevation the SAME reply granted travels in its report, so the
      // client's elevation state never depends on the follow-up refresh. That
      // refresh swallows its own failure, so recovering the window from it
      // would prompt the operator for the password they just chose.
      expect(mockSetAuth).toHaveBeenCalledWith({
        user: { id: 'solo-id', username: 'solo' },
        elevationExpiresAt: soloElevation,
      })
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

  // The password is stored, but the reply could not report the account. The
  // adoption starts sign-in recovery, which replaces this screen and owns the
  // account, so the gate reports no failure and does not re-read the account.
  // It still re-reads the hub: recovery ends on a route this gate guards, and a
  // snapshot that still asks for the password would show this screen again for
  // a password the hub refuses to set twice.
  it('leaves a recovering adoption to sign-in recovery', async () => {
    mockSetInitialSoloPassword.mockResolvedValue({
      sessionReport: { elevationExpiresAt: soloElevation },
    })
    mockSetAuth.mockResolvedValue('recovering')
    render(() => <PasswordSetupGate />)
    fillPassword()

    fireEvent.click(screen.getByRole('button', { name: 'Set Password' }))

    await vi.waitFor(() => expect(mockSetAuth).toHaveBeenCalledWith({ elevationExpiresAt: soloElevation }))
    await vi.waitFor(() => expect(mockLoadSystemInfo).toHaveBeenCalledWith(true))
    expect(mockRefreshUser).not.toHaveBeenCalled()
    expect(screen.queryByText(/Failed to set the password/)).toBeNull()
  })

  // A newer auth operation replaced this adoption, so the account belongs to
  // it. The gate does not re-read an account it did not adopt, and it reports
  // no failure for a password the hub stored. It still re-reads the hub, so
  // that the stored password takes this screen down.
  it('leaves a superseded adoption to the newer operation', async () => {
    mockSetAuth.mockResolvedValue('superseded')
    render(() => <PasswordSetupGate />)
    fillPassword()

    fireEvent.click(screen.getByRole('button', { name: 'Set Password' }))

    await vi.waitFor(() => expect(mockSetAuth).toHaveBeenCalledOnce())
    await settle()
    expect(mockRefreshUser).not.toHaveBeenCalled()
    expect(mockLoadSystemInfo).toHaveBeenCalledWith(true)
    expect(screen.queryByText(/Failed to set the password/)).toBeNull()
    expect(screen.queryByText(/The password is set/)).toBeNull()
    // The gate can stay on screen, so the form must be usable again rather
    // than stuck on "Setting...".
    expect(screen.getByRole('button', { name: 'Set Password' })).toBeEnabled()
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

  // A superseded adoption can leave this gate on screen, so a failed re-read
  // there must say the same thing, and give the form back.
  it('says the password landed when the re-read after a superseded adoption failed', async () => {
    mockSetAuth.mockResolvedValue('superseded')
    mockLoadSystemInfo.mockRejectedValue(new Error('connection reset'))
    render(() => <PasswordSetupGate />)
    fillPassword()

    fireEvent.click(screen.getByRole('button', { name: 'Set Password' }))

    expect(await screen.findByText(/The password is set/)).toBeInTheDocument()
    expect(screen.queryByText(/Failed to set the password/)).toBeNull()
    expect(mockRefreshUser).not.toHaveBeenCalled()
    expect(screen.getByRole('button', { name: 'Set Password' })).toBeEnabled()
  })

  it('explains the restricted TCP setup state', () => {
    render(() => <PasswordSetupGate />)
    expect(screen.getByTestId('password-setup-gate')).toHaveTextContent(
      'Until then, TCP callers can only complete this setup',
    )
  })
})
