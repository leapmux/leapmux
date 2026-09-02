import { fireEvent, render, screen } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { mockLoadSystemInfo } from '~/test-support/systemInfoMock'
import { PasswordSetupGate } from './PasswordSetupGate'

const mockChangePassword = vi.fn()

vi.mock('~/api/clients', () => ({
  userClient: { changePassword: (...args: unknown[]) => mockChangePassword(...args) },
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
    mockChangePassword.mockResolvedValue({})
    // Restated, not merely cleared: `clearAllMocks` forgets the CALLS and
    // keeps the implementation, so one test's `mockRejectedValue` would
    // otherwise reject in every test after it.
    mockLoadSystemInfo.mockResolvedValue(undefined)
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
      expect(mockChangePassword).toHaveBeenCalledWith({ newPassword: 'correct-horse-battery-staple' })
    })
    // FORCED, because the gate reads exactly this snapshot: without it the
    // screen stays up over a hub that no longer needs it.
    expect(mockLoadSystemInfo).toHaveBeenCalledWith(true)
  })

  it('reports a refusal rather than leaving the form silent', async () => {
    mockChangePassword.mockRejectedValue(new Error('password is too short'))
    render(() => <PasswordSetupGate />)
    fillPassword()

    fireEvent.click(screen.getByRole('button', { name: 'Set Password' }))

    expect(await screen.findByText(/password is too short/)).toBeInTheDocument()
    // The gate stays up: the hub still has no password, so the exposure stands.
    expect(screen.getByRole('button', { name: 'Set Password' })).toBeInTheDocument()
  })

  // The exposed address is deliberately NOT named: the hub can answer on
  // several, and the one this page was opened at is often not the one that
  // exposes it.
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
    expect(mockChangePassword).toHaveBeenCalledTimes(1)
  })

  it('does not claim which address exposes the hub', () => {
    render(() => <PasswordSetupGate />)
    expect(screen.getByTestId('password-setup-gate')).toHaveTextContent(
      'This hub answers on an address other machines can reach',
    )
  })
})
