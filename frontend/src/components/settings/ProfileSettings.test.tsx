import { fireEvent, render, screen, within } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { ProfileSettings } from './ProfileSettings'

const mockListPasskeys = vi.fn()
const mockDeletePasskey = vi.fn()
const mockDeactivatePasskeyAuth = vi.fn()
const mockRenamePasskey = vi.fn()
const mockChangePassword = vi.fn()
const mockSignalPasskeyRemoved = vi.fn()
const mockSignalAcceptedPasskeys = vi.fn()
const mockObtainPasskeyReauthProof = vi.fn()
const mockBeginPasskeyRegistration = vi.fn()
const mockFinishPasskeyRegistration = vi.fn()
const mockStartRegistration = vi.fn()
const mockUser = vi.fn()
const mockRefreshUser = vi.fn()

vi.mock('~/api/clients', () => ({
  userClient: {
    listPasskeys: (...args: unknown[]) => mockListPasskeys(...args),
    updateProfile: vi.fn(),
    requestEmailChange: vi.fn(),
    changePassword: (...args: unknown[]) => mockChangePassword(...args),
    unlinkOAuthProvider: vi.fn(),
    renamePasskey: (...args: unknown[]) => mockRenamePasskey(...args),
    deletePasskey: (...args: unknown[]) => mockDeletePasskey(...args),
    deactivatePasskeyAuth: (...args: unknown[]) => mockDeactivatePasskeyAuth(...args),
    beginPasskeyRegistration: (...args: unknown[]) => mockBeginPasskeyRegistration(...args),
    finishPasskeyRegistration: (...args: unknown[]) => mockFinishPasskeyRegistration(...args),
    beginPasskeyReauth: vi.fn(),
    finishPasskeyReauth: vi.fn(),
  },
}))

vi.mock('~/context/AuthContext', () => ({
  useAuth: () => ({
    user: () => mockUser(),
    refreshUser: mockRefreshUser,
  }),
}))

vi.mock('~/lib/webauthn', () => ({
  startRegistration: (...args: unknown[]) => mockStartRegistration(...args),
  startAuthentication: vi.fn(),
  signalPasskeyRemoved: (...args: unknown[]) => mockSignalPasskeyRemoved(...args),
  signalAcceptedPasskeys: (...args: unknown[]) => mockSignalAcceptedPasskeys(...args),
}))

vi.mock('~/lib/passkeyManagement', async () => {
  const actual = await vi.importActual<typeof import('~/lib/passkeyManagement')>('~/lib/passkeyManagement')
  return {
    ...actual,
    loadPasskeys: async () => {
      const resp = await mockListPasskeys({})
      return { passkeys: resp.passkeys ?? [], rpId: resp.rpId ?? 'localhost' }
    },
    obtainPasskeyReauthProof: (...args: unknown[]) => mockObtainPasskeyReauthProof(...args),
  }
})

describe('profileSettings passkeys', () => {
  beforeEach(() => {
    HTMLDialogElement.prototype.showModal = vi.fn(function (this: HTMLDialogElement) {
      this.open = true
    })
    HTMLDialogElement.prototype.close = vi.fn(function (this: HTMLDialogElement) {
      this.open = false
    })
    vi.clearAllMocks()
    mockRefreshUser.mockResolvedValue(undefined)
    mockUser.mockReturnValue({
      id: 'user-1',
      username: 'alice',
      displayName: 'Alice',
      email: 'alice@example.com',
      emailVerified: true,
      passwordSet: true,
      passkeyCount: 1,
      oauthProviders: [],
    })
    mockListPasskeys.mockResolvedValue({
      passkeys: [{ id: 'pk-1', friendlyName: 'Laptop', transports: [], credentialId: 'cred-abc' }],
      rpId: 'localhost',
    })
    mockDeletePasskey.mockResolvedValue({})
    mockDeactivatePasskeyAuth.mockResolvedValue({})
    mockRenamePasskey.mockResolvedValue({})
    mockChangePassword.mockResolvedValue({})
    mockObtainPasskeyReauthProof.mockResolvedValue('proof-123')
    mockBeginPasskeyRegistration.mockResolvedValue({ sessionId: 'sess-1', optionsJson: '{}' })
    mockStartRegistration.mockResolvedValue('{"id":"cred-new"}')
    mockFinishPasskeyRegistration.mockResolvedValue({
      passkey: { id: 'pk-2', friendlyName: 'Phone', transports: [], credentialId: 'cred-new' },
    })
  })

  it('lists passkeys and offers password-gated add flow', async () => {
    render(() => <ProfileSettings />)
    expect(await screen.findByText('Laptop')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Add passkey' }))
    expect(await screen.findByLabelText('Current password')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Continue' })).toBeDisabled()
    fireEvent.input(screen.getByLabelText('Current password'), { target: { value: 'secret' } })
    expect(screen.getByRole('button', { name: 'Continue' })).toBeEnabled()
  })

  it('deletes a passkey with the current password and notifies Signal API', async () => {
    render(() => <ProfileSettings />)
    expect(await screen.findByText('Laptop')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Remove' }))
    fireEvent.input(await screen.findByLabelText('Current password'), { target: { value: 'secret' } })
    fireEvent.click(screen.getByRole('button', { name: 'Remove passkey' }))
    await vi.waitFor(() => {
      expect(mockDeletePasskey).toHaveBeenCalledWith(expect.objectContaining({
        id: 'pk-1',
        currentPassword: 'secret',
      }))
    })
    expect(mockSignalPasskeyRemoved).toHaveBeenCalledWith('localhost', 'cred-abc')
  })

  it('deactivates passkey sign-in with the current password and notifies Signal API', async () => {
    render(() => <ProfileSettings />)
    expect(await screen.findByText('Laptop')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Disable passkey sign-in' }))
    fireEvent.input(await screen.findByLabelText('Current password'), { target: { value: 'secret' } })
    const confirmButtons = screen.getAllByRole('button', { name: 'Disable passkey sign-in' })
    fireEvent.click(confirmButtons[confirmButtons.length - 1]!)
    await vi.waitFor(() => {
      expect(mockDeactivatePasskeyAuth).toHaveBeenCalledWith(expect.objectContaining({
        currentPassword: 'secret',
      }))
    })
    expect(mockSignalPasskeyRemoved).toHaveBeenCalledWith('localhost', 'cred-abc')
  })

  it('offers reauth-gated add flow when password is not set', async () => {
    mockUser.mockReturnValue({
      username: 'alice',
      displayName: 'Alice',
      email: 'alice@example.com',
      emailVerified: true,
      passwordSet: false,
      passkeyCount: 1,
      oauthProviders: [],
    })

    render(() => <ProfileSettings />)
    expect(await screen.findByText('Laptop')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Add passkey' }))
    expect(screen.queryByLabelText('Current password')).not.toBeInTheDocument()
    const verifyButtons = await screen.findAllByRole('button', { name: 'Verify with passkey' })
    // Password section + add-passkey modal both offer Verify when !passwordSet.
    expect(verifyButtons.length).toBeGreaterThanOrEqual(2)
  })

  it('deletes a non-last passkey with reauth when password is not set', async () => {
    mockUser.mockReturnValue({
      username: 'alice',
      displayName: 'Alice',
      email: 'alice@example.com',
      emailVerified: true,
      passwordSet: false,
      passkeyCount: 2,
      oauthProviders: [],
    })
    mockListPasskeys.mockResolvedValue({
      passkeys: [
        { id: 'pk-1', friendlyName: 'Laptop', transports: [], credentialId: 'cred-abc' },
        { id: 'pk-2', friendlyName: 'Phone', transports: [], credentialId: 'cred-def' },
      ],
    })

    render(() => <ProfileSettings />)
    expect(await screen.findByText('Phone')).toBeInTheDocument()
    const removeButtons = screen.getAllByRole('button', { name: 'Remove' })
    fireEvent.click(removeButtons[1]!)
    const verifyButtons = await screen.findAllByRole('button', { name: 'Verify with passkey' })
    fireEvent.click(verifyButtons[verifyButtons.length - 1]!)
    await vi.waitFor(() => {
      expect(mockObtainPasskeyReauthProof).toHaveBeenCalled()
    })
    fireEvent.click(screen.getByRole('button', { name: 'Remove passkey' }))
    await vi.waitFor(() => {
      expect(mockDeletePasskey).toHaveBeenCalledWith(expect.objectContaining({
        id: 'pk-2',
        reauthProof: 'proof-123',
      }))
    })
  })

  it('shows verify UI when removing the last passkey without a password', async () => {
    mockUser.mockReturnValue({
      username: 'alice',
      displayName: 'Alice',
      email: 'alice@example.com',
      emailVerified: true,
      passwordSet: false,
      passkeyCount: 1,
      oauthProviders: [],
    })

    render(() => <ProfileSettings />)
    expect(await screen.findByText('Laptop')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Remove' }))
    expect(await screen.findByText(/only sign-in method/i)).toBeInTheDocument()
    expect(await screen.findByLabelText('New password')).toBeInTheDocument()
    const verifyButtons = await screen.findAllByRole('button', { name: 'Verify with passkey' })
    // Password section + last-passkey delete modal.
    expect(verifyButtons.length).toBeGreaterThanOrEqual(2)
  })

  it('offers verify with passkey when setting a first password and passkeys exist', async () => {
    mockUser.mockReturnValue({
      username: 'alice',
      displayName: 'Alice',
      email: 'alice@example.com',
      emailVerified: true,
      passwordSet: false,
      passkeyCount: 1,
      oauthProviders: [],
    })

    render(() => <ProfileSettings />)
    expect(await screen.findByText('Laptop')).toBeInTheDocument()
    const verifyButtons = await screen.findAllByRole('button', { name: 'Verify with passkey' })
    // Password section shows Verify when !passwordSet and passkeys exist.
    expect(verifyButtons.length).toBe(1)
  })

  it('hides password-section verify for OAuth-only users with zero passkeys', async () => {
    mockUser.mockReturnValue({
      username: 'alice',
      displayName: 'Alice',
      email: 'alice@example.com',
      emailVerified: true,
      passwordSet: false,
      passkeyCount: 0,
      oauthProviders: [{ id: 'github', name: 'GitHub' }],
    })
    mockListPasskeys.mockResolvedValue({ passkeys: [] })

    render(() => <ProfileSettings />)
    await vi.waitFor(() => {
      expect(screen.getByRole('button', { name: 'Set Password' })).toBeInTheDocument()
    })
    expect(screen.queryByRole('button', { name: 'Verify with passkey' })).not.toBeInTheDocument()
  })

  it('hides verify in add-passkey modal for OAuth-only users with zero passkeys', async () => {
    mockUser.mockReturnValue({
      username: 'alice',
      displayName: 'Alice',
      email: 'alice@example.com',
      emailVerified: true,
      passwordSet: false,
      passkeyCount: 0,
      oauthProviders: [{ id: 'github', name: 'GitHub' }],
    })
    mockListPasskeys.mockResolvedValue({ passkeys: [] })

    render(() => <ProfileSettings />)
    expect(await screen.findByText('No passkeys registered yet.')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Add passkey' }))
    expect(await screen.findByLabelText('Name')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Verify with passkey' })).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Continue' })).toBeInTheDocument()
  })

  it('renames a passkey with the current password', async () => {
    render(() => <ProfileSettings />)
    expect(await screen.findByText('Laptop')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Rename' }))
    fireEvent.input(screen.getByPlaceholderText('Current password'), { target: { value: 'secret' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await vi.waitFor(() => {
      expect(mockRenamePasskey).toHaveBeenCalledWith(expect.objectContaining({
        id: 'pk-1',
        currentPassword: 'secret',
      }))
    })
  })

  it('updates password-section verify from live passkey list count', async () => {
    mockUser.mockReturnValue({
      username: 'alice',
      displayName: 'Alice',
      email: 'alice@example.com',
      emailVerified: true,
      passwordSet: false,
      passkeyCount: 0,
      oauthProviders: [{ id: 'github', name: 'GitHub' }],
    })
    mockListPasskeys.mockResolvedValue({
      passkeys: [{ id: 'pk-1', friendlyName: 'Laptop', transports: [], credentialId: 'cred-abc' }],
    })

    render(() => <ProfileSettings />)
    expect(await screen.findByText('Laptop')).toBeInTheDocument()
    // auth.user().passkeyCount is still 0; live list drives the verify button.
    const verifyButtons = await screen.findAllByRole('button', { name: 'Verify with passkey' })
    expect(verifyButtons.length).toBeGreaterThanOrEqual(1)
  })

  it('deactivates passkey-only auth with reauth and a new password', async () => {
    mockUser.mockReturnValue({
      username: 'alice',
      displayName: 'Alice',
      email: 'alice@example.com',
      emailVerified: true,
      passwordSet: false,
      passkeyCount: 1,
      oauthProviders: [],
    })

    render(() => <ProfileSettings />)
    expect(await screen.findByText('Laptop')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Disable passkey sign-in' }))
    fireEvent.input(await screen.findByLabelText('New password'), { target: { value: 'newpass123' } })
    fireEvent.input(await screen.findByLabelText('Confirm password'), { target: { value: 'newpass123' } })
    const verifyButtons = await screen.findAllByRole('button', { name: 'Verify with passkey' })
    fireEvent.click(verifyButtons[verifyButtons.length - 1]!)
    await vi.waitFor(() => {
      expect(mockObtainPasskeyReauthProof).toHaveBeenCalled()
    })
    const confirmButtons = screen.getAllByRole('button', { name: 'Disable passkey sign-in' })
    fireEvent.click(confirmButtons[confirmButtons.length - 1]!)
    await vi.waitFor(() => {
      expect(mockDeactivatePasskeyAuth).toHaveBeenCalledWith(expect.objectContaining({
        reauthProof: 'proof-123',
        newPassword: 'newpass123',
      }))
    })
    expect(mockSignalPasskeyRemoved).toHaveBeenCalledWith('localhost', 'cred-abc')
  })

  it('keeps Set Password disabled until passkey verification succeeds', async () => {
    mockUser.mockReturnValue({
      username: 'alice',
      displayName: 'Alice',
      email: 'alice@example.com',
      emailVerified: true,
      passwordSet: false,
      passkeyCount: 1,
      oauthProviders: [],
    })

    render(() => <ProfileSettings />)
    expect(await screen.findByText('Laptop')).toBeInTheDocument()
    fireEvent.input(screen.getByLabelText('New Password'), { target: { value: 'newpass123' } })
    fireEvent.input(screen.getByLabelText('Confirm Password'), { target: { value: 'newpass123' } })
    expect(screen.getByRole('button', { name: 'Set Password' })).toBeDisabled()

    fireEvent.click(screen.getByRole('button', { name: 'Verify with passkey' }))
    await vi.waitFor(() => {
      expect(mockObtainPasskeyReauthProof).toHaveBeenCalled()
    })
    expect(await screen.findByRole('button', { name: 'Set Password' })).toBeEnabled()
  })

  it('shows current-password add flow after set-password refresh', async () => {
    mockUser.mockReturnValue({
      username: 'alice',
      displayName: 'Alice',
      email: 'alice@example.com',
      emailVerified: true,
      passwordSet: false,
      passkeyCount: 1,
      oauthProviders: [],
    })
    mockChangePassword.mockImplementation(async () => {
      mockUser.mockReturnValue({
        username: 'alice',
        displayName: 'Alice',
        email: 'alice@example.com',
        emailVerified: true,
        passwordSet: true,
        passkeyCount: 1,
        oauthProviders: [],
      })
    })

    render(() => <ProfileSettings />)
    expect(await screen.findByText('Laptop')).toBeInTheDocument()
    fireEvent.input(screen.getByLabelText('New Password'), { target: { value: 'newpass123' } })
    fireEvent.input(screen.getByLabelText('Confirm Password'), { target: { value: 'newpass123' } })
    fireEvent.click(screen.getByRole('button', { name: 'Verify with passkey' }))
    await vi.waitFor(() => {
      expect(mockObtainPasskeyReauthProof).toHaveBeenCalled()
    })
    fireEvent.click(screen.getByRole('button', { name: 'Set Password' }))
    await vi.waitFor(() => {
      expect(mockChangePassword).toHaveBeenCalled()
    })
    await vi.waitFor(() => {
      expect(mockRefreshUser).toHaveBeenCalled()
    })

    fireEvent.click(screen.getByRole('button', { name: 'Add passkey' }))
    expect(await screen.findByLabelText('Current password')).toBeInTheDocument()
    const addDialog = screen.getByRole('dialog', { name: 'Add passkey' })
    expect(within(addDialog).queryByRole('button', { name: 'Verify with passkey' })).not.toBeInTheDocument()
  })

  it('renames a passkey with reauth when password is not set', async () => {
    mockUser.mockReturnValue({
      username: 'alice',
      displayName: 'Alice',
      email: 'alice@example.com',
      emailVerified: true,
      passwordSet: false,
      passkeyCount: 1,
      oauthProviders: [],
    })

    render(() => <ProfileSettings />)
    expect(await screen.findByText('Laptop')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Rename' }))
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled()
    const verifyButtons = screen.getAllByRole('button', { name: 'Verify with passkey' })
    fireEvent.click(verifyButtons[verifyButtons.length - 1]!)
    await vi.waitFor(() => {
      expect(mockObtainPasskeyReauthProof).toHaveBeenCalled()
    })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await vi.waitFor(() => {
      expect(mockRenamePasskey).toHaveBeenCalledWith(expect.objectContaining({
        id: 'pk-1',
        reauthProof: 'proof-123',
      }))
    })
  })

  it('keeps last-passkey remove disabled until the new password matches', async () => {
    mockUser.mockReturnValue({
      username: 'alice',
      displayName: 'Alice',
      email: 'alice@example.com',
      emailVerified: true,
      passwordSet: false,
      passkeyCount: 1,
      oauthProviders: [],
    })

    render(() => <ProfileSettings />)
    expect(await screen.findByText('Laptop')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Remove' }))
    expect(await screen.findByRole('button', { name: 'Remove passkey' })).toBeDisabled()
    fireEvent.input(screen.getByLabelText('New password'), { target: { value: 'newpass123' } })
    fireEvent.input(screen.getByLabelText('Confirm password'), { target: { value: 'newpass123' } })
    expect(screen.getByRole('button', { name: 'Remove passkey' })).toBeDisabled()
    const verifyButtons = screen.getAllByRole('button', { name: 'Verify with passkey' })
    fireEvent.click(verifyButtons[verifyButtons.length - 1]!)
    await vi.waitFor(() => {
      expect(mockObtainPasskeyReauthProof).toHaveBeenCalled()
    })
    expect(screen.getByRole('button', { name: 'Remove passkey' })).toBeEnabled()
  })

  it('keeps the added passkey in the list when the follow-up refresh fails', async () => {
    mockUser.mockReturnValue({
      id: 'user-1',
      username: 'alice',
      displayName: 'Alice',
      email: 'alice@example.com',
      emailVerified: true,
      passwordSet: true,
      passkeyCount: 1,
      oauthProviders: [],
    })
    mockListPasskeys
      .mockResolvedValueOnce({
        passkeys: [{ id: 'pk-1', friendlyName: 'Laptop', transports: [], credentialId: 'cred-abc' }],
      })
      .mockRejectedValue(new Error('list failed'))

    render(() => <ProfileSettings />)
    expect(await screen.findByText('Laptop')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Add passkey' }))
    fireEvent.input(await screen.findByLabelText('Current password'), { target: { value: 'secret' } })
    fireEvent.click(screen.getByRole('button', { name: 'Continue' }))
    expect(await screen.findByText('Phone')).toBeInTheDocument()
    expect(screen.getByText('Laptop')).toBeInTheDocument()
    expect(screen.getByText('Passkey added.')).toBeInTheDocument()
    await vi.waitFor(() => {
      expect(mockSignalAcceptedPasskeys).toHaveBeenCalledWith(
        'localhost',
        'user-1',
        expect.arrayContaining(['cred-abc', 'cred-new']),
      )
    })
  })

  it('keeps the passkey removed from the list when the follow-up refresh fails', async () => {
    mockListPasskeys
      .mockResolvedValueOnce({
        passkeys: [{ id: 'pk-1', friendlyName: 'Laptop', transports: [], credentialId: 'cred-abc' }],
      })
      .mockRejectedValue(new Error('list failed'))

    render(() => <ProfileSettings />)
    expect(await screen.findByText('Laptop')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Remove' }))
    fireEvent.input(await screen.findByLabelText('Current password'), { target: { value: 'secret' } })
    fireEvent.click(screen.getByRole('button', { name: 'Remove passkey' }))
    await vi.waitFor(() => {
      expect(screen.queryByText('Laptop')).not.toBeInTheDocument()
    })
    expect(screen.getByText('Passkey removed.')).toBeInTheDocument()
  })
})
