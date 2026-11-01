import { fireEvent, render, screen } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { AccountProfile } from './AccountProfile'

const mockUpdateProfile = vi.fn()
const mockUser = vi.fn()
const mockRefreshUser = vi.fn()
// The adoption the page opens before the write leaves. The order records when
// it opened relative to the RPC.
const callOrder: string[] = []
const mockAdopt = vi.fn<(user: unknown) => Promise<void>>(async () => {})
const mockOpenUserAdoption = vi.fn(() => {
  callOrder.push('open')
  return mockAdopt
})

vi.mock('~/api/clients', () => ({
  userClient: {
    updateProfile: (...args: unknown[]) => mockUpdateProfile(...args),
  },
}))

vi.mock('~/context/AuthContext', () => ({
  useAuth: () => ({
    user: () => mockUser(),
    refreshUser: mockRefreshUser,
    openUserAdoption: mockOpenUserAdoption,
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

describe('AccountProfile', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mockUser.mockReturnValue(alice)
    mockRefreshUser.mockResolvedValue(undefined)
    mockUpdateProfile.mockResolvedValue({})
    callOrder.length = 0
  })

  // Save is the only thing that writes, and nothing is dirty on mount, so a
  // live button here would send the stored values straight back.
  it('offers nothing to save until a field changes', async () => {
    render(() => <AccountProfile />)
    const save = await screen.findByRole('button', { name: 'Save Profile' })
    expect(save).toBeDisabled()

    fireEvent.input(screen.getByLabelText('Display Name'), {
      target: { value: 'Alice B' },
    })
    expect(save).toBeEnabled()
  })

  it('sends both fields in one write', async () => {
    render(() => <AccountProfile />)
    fireEvent.input(await screen.findByLabelText('Display Name'), {
      target: { value: 'Alice B' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Save Profile' }))

    await vi.waitFor(() => {
      expect(mockUpdateProfile).toHaveBeenCalledWith({ username: 'alice', displayName: 'Alice B' })
    })
    expect(await screen.findByText('Profile updated.')).toBeInTheDocument()
  })

  // The hub sanitizes both fields again, so the page adopts the account it
  // STORED, as the reply reports it. The adoption opens BEFORE the write
  // leaves, so a reply that lands after an identity change re-reads instead.
  it('adopts the stored account the reply reports, through an adoption opened first', async () => {
    const stored = { ...alice, displayName: 'Alice B (stored)' }
    mockUpdateProfile.mockImplementation(async () => {
      callOrder.push('update')
      return { user: stored }
    })
    render(() => <AccountProfile />)
    fireEvent.input(await screen.findByLabelText('Display Name'), {
      target: { value: 'Alice B' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Save Profile' }))

    await vi.waitFor(() => expect(mockAdopt).toHaveBeenCalledWith(stored))
    expect(callOrder).toEqual(['open', 'update'])
    expect(mockRefreshUser).not.toHaveBeenCalled()
  })

  // The form shows what the adoption left on the account, which is what the
  // hub stored, and not the text the user typed.
  it('shows the stored values after the adoption', async () => {
    const stored = { ...alice, displayName: 'Alice B (stored)' }
    mockUpdateProfile.mockResolvedValue({ user: stored })
    mockAdopt.mockImplementationOnce(async (user) => {
      mockUser.mockReturnValue(user)
    })
    render(() => <AccountProfile />)
    fireEvent.input(await screen.findByLabelText('Display Name'), {
      target: { value: 'Alice B' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Save Profile' }))

    expect(await screen.findByText('Profile updated.')).toBeInTheDocument()
    expect(screen.getByLabelText('Display Name')).toHaveValue('Alice B (stored)')
    expect(screen.getByLabelText('Username')).toHaveValue('alice')
  })

  // A reply with no account hands the adoption nothing to adopt, so the
  // adoption re-reads the account. The write itself still succeeded.
  it('hands an absent user to the adoption and reports the update', async () => {
    mockUpdateProfile.mockResolvedValue({})
    render(() => <AccountProfile />)
    fireEvent.input(await screen.findByLabelText('Display Name'), {
      target: { value: 'Alice B' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Save Profile' }))

    expect(await screen.findByText('Profile updated.')).toBeInTheDocument()
    expect(mockAdopt).toHaveBeenCalledExactlyOnceWith(undefined)
  })

  it('refuses a display name the validator rejects, and says so', async () => {
    render(() => <AccountProfile />)
    // Over the 128-byte name limit, which `sanitizeName` refuses. An EMPTY
    // display name is deliberately allowed (it falls back to the username), so
    // the empty string cannot stand in for "invalid".
    fireEvent.input(await screen.findByLabelText('Display Name'), {
      target: {
        value: 'A'.repeat(129),
      },
    })

    expect(await screen.findByText(/at most 128 bytes/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Save Profile' })).toBeDisabled()
    expect(mockUpdateProfile).not.toHaveBeenCalled()
  })

  // An empty display name is not an error: the hub falls back to the username.
  // A rule that treated blank as invalid would make the field impossible to
  // clear.
  it('allows an empty display name', async () => {
    render(() => <AccountProfile />)
    fireEvent.input(await screen.findByLabelText('Display Name'), {
      target: { value: '' },
    })

    expect(screen.getByRole('button', { name: 'Save Profile' })).toBeEnabled()
    fireEvent.click(screen.getByRole('button', { name: 'Save Profile' }))
    await vi.waitFor(() => {
      expect(mockUpdateProfile).toHaveBeenCalledWith({ username: 'alice', displayName: 'alice' })
    })
  })

  it('reports a failed write instead of claiming success', async () => {
    mockUpdateProfile.mockRejectedValue(new Error('username is taken'))
    render(() => <AccountProfile />)
    fireEvent.input(await screen.findByLabelText('Username'), {
      target: { value: 'bob' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Save Profile' }))

    expect(await screen.findByText(/username is taken/)).toBeInTheDocument()
    expect(screen.queryByText('Profile updated.')).not.toBeInTheDocument()
    // A refused write stored nothing, so the page adopts nothing.
    expect(mockAdopt).not.toHaveBeenCalled()
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
