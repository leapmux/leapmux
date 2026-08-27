import { fireEvent, render, screen } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { AccountPassword } from './AccountPassword'

const mockChangePassword = vi.fn()
const mockUser = vi.fn()
const mockRefreshUser = vi.fn()

vi.mock('~/api/clients', () => ({
  userClient: {
    changePassword: (...args: unknown[]) => mockChangePassword(...args),
  },
}))

vi.mock('~/context/AuthContext', () => ({
  useAuth: () => ({
    user: () => mockUser(),
    refreshUser: mockRefreshUser,
  }),
}))

const withPassword = {
  id: 'user-1',
  username: 'alice',
  displayName: 'Alice',
  email: 'alice@example.com',
  emailVerified: true,
  passwordSet: true,
  passkeyCount: 1,
  oauthProviders: [],
}

const withoutPassword = { ...withPassword, passwordSet: false }

describe('accountPassword', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mockUser.mockReturnValue(withPassword)
    mockRefreshUser.mockResolvedValue(undefined)
    mockChangePassword.mockResolvedValue({})
  })

  // The step-up is the SESSION's elevation, proved once in the prompt, not a
  // secret re-typed per action. A "current password" field here would ask for
  // one that nothing verifies.
  it('asks for no current password', async () => {
    render(() => <AccountPassword />)
    expect(await screen.findByLabelText('New Password')).toBeInTheDocument()
    expect(screen.queryByLabelText('Current Password')).not.toBeInTheDocument()
  })

  it('changes the password once both fields agree', async () => {
    render(() => <AccountPassword />)
    fireEvent.input(await screen.findByLabelText('New Password'), { target: { value: 'newpass123' } })
    fireEvent.input(screen.getByLabelText('Confirm Password'), { target: { value: 'newpass123' } })
    fireEvent.click(screen.getByRole('button', { name: 'Change Password' }))

    await vi.waitFor(() => {
      expect(mockChangePassword).toHaveBeenCalledWith({ newPassword: 'newpass123' })
    })
    expect(await screen.findByText('Password changed.')).toBeInTheDocument()
  })

  it('refuses to submit while the two fields disagree', async () => {
    render(() => <AccountPassword />)
    fireEvent.input(await screen.findByLabelText('New Password'), { target: { value: 'newpass123' } })
    fireEvent.input(screen.getByLabelText('Confirm Password'), { target: { value: 'newpass124' } })

    expect(screen.getByRole('button', { name: 'Change Password' })).toBeDisabled()
    expect(mockChangePassword).not.toHaveBeenCalled()
  })

  // An account with no password SETS one, and the button must say which of
  // the two it is about to do.
  it('says "Set Password" for an account that has none', async () => {
    mockUser.mockReturnValue(withoutPassword)
    render(() => <AccountPassword />)
    expect(await screen.findByRole('button', { name: 'Set Password' })).toBeInTheDocument()

    fireEvent.input(screen.getByLabelText('New Password'), { target: { value: 'newpass123' } })
    fireEvent.input(screen.getByLabelText('Confirm Password'), { target: { value: 'newpass123' } })
    fireEvent.click(screen.getByRole('button', { name: 'Set Password' }))

    expect(await screen.findByText('Password set.')).toBeInTheDocument()
  })

  it('reports a refusal and keeps the typed value', async () => {
    mockChangePassword.mockRejectedValue(new Error('password is too common'))
    render(() => <AccountPassword />)
    // The element is captured BEFORE typing: `PasswordFields` renders a live
    // strength readout inside the same <label>, so the label's accessible name
    // stops being exactly "New Password" as soon as the field is non-empty.
    const password = await screen.findByLabelText('New Password')
    fireEvent.input(password, { target: { value: 'newpass123' } })
    fireEvent.input(screen.getByLabelText('Confirm Password'), { target: { value: 'newpass123' } })
    fireEvent.click(screen.getByRole('button', { name: 'Change Password' }))

    expect(await screen.findByText(/too common/)).toBeInTheDocument()
    expect(password).toHaveValue('newpass123')
  })

  it('clears both fields after a successful change', async () => {
    render(() => <AccountPassword />)
    fireEvent.input(await screen.findByLabelText('New Password'), { target: { value: 'newpass123' } })
    fireEvent.input(screen.getByLabelText('Confirm Password'), { target: { value: 'newpass123' } })
    fireEvent.click(screen.getByRole('button', { name: 'Change Password' }))

    await screen.findByText('Password changed.')
    expect(screen.getByLabelText('New Password')).toHaveValue('')
    expect(screen.getByLabelText('Confirm Password')).toHaveValue('')
  })
})
