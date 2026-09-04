import { fireEvent, render, screen } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { mockLoadSystemInfo, resetSystemInfoMock, setSystemInfoMock } from '~/test-support/systemInfoMock'
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

vi.mock('~/lib/systemInfo', async () => {
  const m = await import('~/test-support/systemInfoMock')
  return m.systemInfoMock
})

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
    resetSystemInfoMock()
    mockUser.mockReturnValue(withPassword)
    mockRefreshUser.mockResolvedValue(undefined)
    mockChangePassword.mockResolvedValue({})
    // Restated, not merely cleared: `clearAllMocks` and `resetSystemInfoMock`
    // both forget the CALLS and keep the implementation, so the rejecting case
    // below would otherwise reject in every test after it.
    mockLoadSystemInfo.mockResolvedValue(undefined)
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

  /** Types a matching pair and submits, whatever the button is called. */
  async function submit(name: 'Set Password' | 'Change Password') {
    fireEvent.input(await screen.findByLabelText('New Password'), { target: { value: 'newpass123' } })
    fireEvent.input(screen.getByLabelText('Confirm Password'), { target: { value: 'newpass123' } })
    fireEvent.click(screen.getByRole('button', { name }))
  }

  // The first password on a solo hub arms a rule that reaches every address,
  // and this is the one surface that can arm it from inside the app. The two
  // others -- the setup gate and the Network access panel -- both say so.
  it('states what the first password does on a solo hub', async () => {
    setSystemInfoMock({ soloMode: true })
    mockUser.mockReturnValue(withoutPassword)
    render(() => <AccountPassword />)

    expect(await screen.findByText(/makes every TCP address require a sign-in as “solo”/))
      .toBeInTheDocument()
  })

  // Replacing a password changes nothing about who is asked for one, and a
  // multi-user hub never had the rule to arm.
  it('states nothing extra once a password exists, or off a solo hub', async () => {
    setSystemInfoMock({ soloMode: true })
    const { unmount } = render(() => <AccountPassword />)
    expect(await screen.findByRole('button', { name: 'Change Password' })).toBeInTheDocument()
    expect(screen.queryByText(/asks every network address/)).not.toBeInTheDocument()
    unmount()

    setSystemInfoMock({ soloMode: false })
    mockUser.mockReturnValue(withoutPassword)
    render(() => <AccountPassword />)
    expect(await screen.findByRole('button', { name: 'Set Password' })).toBeInTheDocument()
    expect(screen.queryByText(/asks every network address/)).not.toBeInTheDocument()
  })

  // This row sets the FIRST password on a solo hub, and the hub's own snapshot
  // carries that fact for Administration → Network access. Leaving the
  // snapshot stale leaves that panel offering the field, and an Apply there
  // replaces the password this row just stored.
  it('re-reads the hub snapshot on a solo hub', async () => {
    setSystemInfoMock({ soloMode: true })
    mockUser.mockReturnValue(withoutPassword)
    render(() => <AccountPassword />)
    await submit('Set Password')

    expect(await screen.findByText('Password set.')).toBeInTheDocument()
    expect(mockRefreshUser).toHaveBeenCalled()
    // FORCED, or the cached snapshot answers with the state before the write.
    expect(mockLoadSystemInfo).toHaveBeenCalledWith(true)
  })

  // A multi-user hub reports `soloPasswordSet` false whatever the account
  // holds, so the read would cost a round trip and move nothing.
  it('leaves the hub snapshot alone off a solo hub', async () => {
    render(() => <AccountPassword />)
    await submit('Change Password')

    expect(await screen.findByText('Password changed.')).toBeInTheDocument()
    expect(mockRefreshUser).toHaveBeenCalled()
    expect(mockLoadSystemInfo).not.toHaveBeenCalled()
  })

  // The hub stored the password before either re-read ran. Reporting a failure
  // here would tell the user to retry a write that landed -- and the retry
  // meets the sign-in rule that the write armed.
  it('reports the change although the snapshot re-read fails', async () => {
    setSystemInfoMock({ soloMode: true })
    mockLoadSystemInfo.mockRejectedValue(new Error('the hub did not answer'))
    render(() => <AccountPassword />)
    await submit('Change Password')

    expect(await screen.findByText('Password changed.')).toBeInTheDocument()
    expect(screen.queryByText(/Failed to change password/)).not.toBeInTheDocument()
  })
})
