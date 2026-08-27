import { fireEvent, render, screen } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { AccountProfile } from './AccountProfile'

const mockUpdateProfile = vi.fn()
const mockUser = vi.fn()
const mockRefreshUser = vi.fn()

vi.mock('~/api/clients', () => ({
  userClient: {
    updateProfile: (...args: unknown[]) => mockUpdateProfile(...args),
  },
}))

vi.mock('~/context/AuthContext', () => ({
  useAuth: () => ({
    user: () => mockUser(),
    refreshUser: mockRefreshUser,
  }),
}))

const alice = {
  id: 'user-1',
  username: 'alice',
  displayName: 'Alice',
  email: 'alice@example.com',
  emailVerified: true,
  passwordSet: true,
  passkeyCount: 0,
  oauthProviders: [],
}

describe('accountProfile', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mockUser.mockReturnValue(alice)
    mockRefreshUser.mockResolvedValue(undefined)
    mockUpdateProfile.mockResolvedValue({})
  })

  // Save is the only thing that writes, and nothing is dirty on mount, so a
  // live button here would send the stored values straight back.
  it('offers nothing to save until a field changes', async () => {
    render(() => <AccountProfile />)
    const save = await screen.findByRole('button', { name: 'Save Profile' })
    expect(save).toBeDisabled()

    fireEvent.input(screen.getByLabelText('Display Name'), { target: { value: 'Alice B' } })
    expect(save).toBeEnabled()
  })

  it('sends both fields in one write', async () => {
    render(() => <AccountProfile />)
    fireEvent.input(await screen.findByLabelText('Display Name'), { target: { value: 'Alice B' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save Profile' }))

    await vi.waitFor(() => {
      expect(mockUpdateProfile).toHaveBeenCalledWith({ username: 'alice', displayName: 'Alice B' })
    })
    expect(await screen.findByText('Profile updated.')).toBeInTheDocument()
  })

  it('refuses a display name the validator rejects, and says so', async () => {
    render(() => <AccountProfile />)
    // Over the 128-byte name limit, which `sanitizeName` refuses. An EMPTY
    // display name is deliberately allowed (it falls back to the username), so
    // the empty string cannot stand in for "invalid".
    fireEvent.input(await screen.findByLabelText('Display Name'), { target: { value: 'A'.repeat(129) } })

    expect(await screen.findByText(/at most 128 bytes/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Save Profile' })).toBeDisabled()
    expect(mockUpdateProfile).not.toHaveBeenCalled()
  })

  // An empty display name is not an error: the hub falls back to the username.
  // A rule that treated blank as invalid would make the field impossible to
  // clear.
  it('allows an empty display name', async () => {
    render(() => <AccountProfile />)
    fireEvent.input(await screen.findByLabelText('Display Name'), { target: { value: '' } })

    expect(screen.getByRole('button', { name: 'Save Profile' })).toBeEnabled()
    fireEvent.click(screen.getByRole('button', { name: 'Save Profile' }))
    await vi.waitFor(() => {
      expect(mockUpdateProfile).toHaveBeenCalledWith({ username: 'alice', displayName: 'alice' })
    })
  })

  it('reports a failed write instead of claiming success', async () => {
    mockUpdateProfile.mockRejectedValue(new Error('username is taken'))
    render(() => <AccountProfile />)
    fireEvent.input(await screen.findByLabelText('Username'), { target: { value: 'bob' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save Profile' }))

    expect(await screen.findByText(/username is taken/)).toBeInTheDocument()
    expect(screen.queryByText('Profile updated.')).not.toBeInTheDocument()
  })

  // An account that did not load yet renders empty fields rather than
  // throwing; the Save button stays inert because nothing is dirty.
  it('renders with no account loaded', async () => {
    mockUser.mockReturnValue(null)
    render(() => <AccountProfile />)
    expect(await screen.findByLabelText('Username')).toHaveValue('')
    expect(screen.getByRole('button', { name: 'Save Profile' })).toBeDisabled()
  })
})
