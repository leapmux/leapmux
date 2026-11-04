import type { Component } from 'solid-js'
import type { AuthState } from '~/context/AuthContext'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { AuthProvider, useAuth } from '~/context/AuthContext'
import { untilTrue } from '~/test-support/async'
import { resetSystemInfoMock } from '~/test-support/systemInfoMock'
import { AccountEmail } from './AccountEmail'
import { AccountLinkedProviders } from './AccountLinkedProviders'
import { AccountPasskeys } from './AccountPasskeys'
import { AccountPassword } from './AccountPassword'

// The surface tests mock `useAuth`, so they can only show that a surface hands
// the reply to the adoption. This file runs each surface against the REAL
// provider, and fails every `GetCurrentUser` after the first. The reply is then
// the only way that the mutation's result can reach `auth.user()`.

const mockGetCurrentUser = vi.fn()
const mockRequestEmailChange = vi.fn()
const mockChangePassword = vi.fn()
const mockUnlinkOAuthProvider = vi.fn()
const mockListPasskeys = vi.fn()
const mockBeginPasskeyRegistration = vi.fn()
const mockFinishPasskeyRegistration = vi.fn()
const mockDeletePasskey = vi.fn()
const mockDeactivatePasskeyAuth = vi.fn()

vi.mock('~/api/clients', () => ({
  authClient: {
    getCurrentUser: (...args: unknown[]) => mockGetCurrentUser(...args),
    logout: vi.fn().mockResolvedValue({}),
  },
  userClient: {
    requestEmailChange: (...args: unknown[]) => mockRequestEmailChange(...args),
    changePassword: (...args: unknown[]) => mockChangePassword(...args),
    unlinkOAuthProvider: (...args: unknown[]) => mockUnlinkOAuthProvider(...args),
    listPasskeys: (...args: unknown[]) => mockListPasskeys(...args),
    beginPasskeyRegistration: (...args: unknown[]) => mockBeginPasskeyRegistration(...args),
    finishPasskeyRegistration: (...args: unknown[]) => mockFinishPasskeyRegistration(...args),
    deletePasskey: (...args: unknown[]) => mockDeletePasskey(...args),
    deactivatePasskeyAuth: (...args: unknown[]) => mockDeactivatePasskeyAuth(...args),
  },
}))

vi.mock('~/api/workerRpc', () => ({
  channelManager: {
    closeAll: vi.fn(),
  },
}))

vi.mock('~/api/platformBridge', () => ({
  desktopFetch: vi.fn(),
  getCapabilities: () => ({ hubTransport: 'direct' }),
  isTauriApp: () => false,
  platformBridge: {
    resetTunnels: vi.fn().mockResolvedValue(undefined),
  },
}))

vi.mock('~/lib/systemInfo', async () => {
  const m = await import('~/test-support/systemInfoMock')
  return m.systemInfoMock
})

vi.mock('~/lib/webauthn', async importOriginal => ({
  ...await importOriginal<typeof import('~/lib/webauthn')>(),
  registerForChallenge: vi.fn().mockResolvedValue({ sessionId: 'sess-1', credentialJson: '{"id":"cred-new"}' }),
  signalPasskeyRemoved: vi.fn(),
  signalAcceptedPasskeys: vi.fn(),
}))

const alice = {
  id: 'user-1',
  username: 'alice',
  displayName: 'Alice',
  email: 'alice@example.com',
  emailVerified: true,
  pendingEmail: '',
  passwordSet: true,
  passkeyCount: 1,
  oauthProviders: [] as { id: string, name: string, enabled: boolean }[],
}

const laptop = {
  id: 'pk-1',
  friendlyName: 'Laptop',
  transports: [],
  credentialId: 'cred-abc',
}

/**
 * Sign in as `account` and show `Surface`. Every `GetCurrentUser` after the
 * sign-in fails, and the returned accessor gives the live context.
 */
async function mountSignedIn(Surface: Component, account: typeof alice): Promise<() => AuthState> {
  mockGetCurrentUser.mockResolvedValue({
    sessionReport: { user: account },
  })
  let captured: AuthState | undefined
  function Capture() {
    captured = useAuth()
    return null
  }
  render(() => (
    <AuthProvider>
      <Capture />
      <Surface />
    </AuthProvider>
  ))
  const auth = () => captured!
  await untilTrue(() => auth().user()?.id === account.id)
  mockGetCurrentUser.mockRejectedValue(new Error('the hub cannot read the account'))
  return auth
}

async function requestEmailChange(address: string) {
  fireEvent.input(await screen.findByLabelText('New Email'), {
    target: { value: address },
  })
  fireEvent.click(screen.getByRole('button', { name: 'Change Email' }))
}

async function submitPassword(submitButton: () => HTMLElement) {
  fireEvent.input(await screen.findByLabelText('New Password'), {
    target: { value: 'newpass123' },
  })
  fireEvent.input(screen.getByLabelText('Confirm Password'), {
    target: { value: 'newpass123' },
  })
  fireEvent.click(submitButton())
}

/** The panel and its dialog hold one button each with this name, and the dialog comes last. */
function lastDisableButton(): HTMLElement {
  return screen.getAllByRole('button', { name: 'Disable passkey sign-in' }).at(-1)!
}

describe('account mutations when GetCurrentUser fails after the write', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    resetSystemInfoMock()
    HTMLDialogElement.prototype.showModal = vi.fn(function (this: HTMLDialogElement) {
      this.open = true
    })
    HTMLDialogElement.prototype.close = vi.fn(function (this: HTMLDialogElement) {
      this.open = false
    })
    mockListPasskeys.mockResolvedValue({
      passkeys: [laptop],
      rpId: 'localhost',
    })
    mockBeginPasskeyRegistration.mockResolvedValue({
      challenge: { sessionId: 'sess-1', optionsJson: '{}' },
    })
  })

  it('shows the address that a direct email change stored', async () => {
    mockRequestEmailChange.mockResolvedValue({
      emailVerification: { verificationRequired: false },
      user: { ...alice, email: 'new@example.com' },
    })
    await mountSignedIn(AccountEmail, alice)

    await requestEmailChange('new@example.com')

    expect(await screen.findByText('Email updated.')).toBeInTheDocument()
    expect(screen.getByText(/^new@example\.com/)).toBeInTheDocument()
  })

  it('shows the pending address that an email change stored', async () => {
    mockRequestEmailChange.mockResolvedValue({
      emailVerification: { verificationEmailSent: true },
      user: { ...alice, pendingEmail: 'new@example.com' },
    })
    await mountSignedIn(AccountEmail, alice)

    await requestEmailChange('new@example.com')

    expect(await screen.findByText(/Check your inbox/)).toBeInTheDocument()
    expect(screen.getByText('new@example.com')).toBeInTheDocument()
  })

  it('offers to change the first password that the write stored', async () => {
    mockChangePassword.mockResolvedValue({
      user: { ...alice, passwordSet: true },
    })
    await mountSignedIn(AccountPassword, { ...alice, passwordSet: false })

    await submitPassword(() => screen.getByRole('button', { name: 'Set Password' }))

    expect(await screen.findByText('Password set.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Change Password' })).toBeInTheDocument()
  })

  it('removes the row of the provider that the write detached', async () => {
    mockUnlinkOAuthProvider.mockResolvedValue({
      user: { ...alice, oauthProviders: [] },
    })
    await mountSignedIn(AccountLinkedProviders, {
      ...alice,
      oauthProviders: [
        { id: 'github-1', name: 'GitHub', enabled: true },
      ],
    })

    fireEvent.click(await screen.findByRole('button', { name: 'Unlink' }))
    fireEvent.click(screen.getByRole('button', { name: 'Unlink?' }))

    expect(await screen.findByText(/signs in with no identity provider/)).toBeInTheDocument()
    expect(screen.queryByText('GitHub')).not.toBeInTheDocument()
  })

  it('counts the passkey that a registration added', async () => {
    mockFinishPasskeyRegistration.mockResolvedValue({
      passkey: {
        id: 'pk-2',
        friendlyName: 'Phone',
        transports: [],
        credentialId: 'cred-new',
      },
      user: { ...alice, passkeyCount: 2 },
    })
    const auth = await mountSignedIn(AccountPasskeys, alice)

    fireEvent.click(await screen.findByRole('button', { name: 'Add passkey' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Continue' }))

    expect(await screen.findByText('Passkey added.')).toBeInTheDocument()
    await untilTrue(() => auth().user()?.passkeyCount === 2)
  })

  it('stops counting the passkey that a delete removed', async () => {
    mockDeletePasskey.mockResolvedValue({
      user: { ...alice, passkeyCount: 0 },
    })
    const auth = await mountSignedIn(AccountPasskeys, alice)

    fireEvent.click(await screen.findByRole('button', { name: 'Remove' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Remove passkey' }))
    fireEvent.click(screen.getByRole('button', { name: 'Confirm?' }))

    expect(await screen.findByText('Passkey removed.')).toBeInTheDocument()
    await untilTrue(() => auth().user()?.passkeyCount === 0)
  })

  it('adopts the password and the count that a deactivation stored', async () => {
    mockDeactivatePasskeyAuth.mockResolvedValue({
      user: { ...alice, passwordSet: true, passkeyCount: 0 },
    })
    const auth = await mountSignedIn(AccountPasskeys, { ...alice, passwordSet: false })

    fireEvent.click(await screen.findByRole('button', { name: 'Disable passkey sign-in' }))
    await submitPassword(lastDisableButton)

    expect(await screen.findByText('Passkey sign-in disabled.')).toBeInTheDocument()
    await untilTrue(() => auth().user()?.passwordSet === true && auth().user()?.passkeyCount === 0)
  })
})
