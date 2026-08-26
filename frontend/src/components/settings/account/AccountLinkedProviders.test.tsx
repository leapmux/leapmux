import { Code, ConnectError } from '@connectrpc/connect'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { AccountLinkedProviders } from './AccountLinkedProviders'

const mockUnlink = vi.fn()
const mockUser = vi.fn()
const mockRefreshUser = vi.fn()

vi.mock('~/api/clients', () => ({
  userClient: {
    unlinkOAuthProvider: (...args: unknown[]) => mockUnlink(...args),
  },
}))

vi.mock('~/context/AuthContext', () => ({
  useAuth: () => ({
    user: () => mockUser(),
    refreshUser: mockRefreshUser,
  }),
}))

const base = {
  id: 'user-1',
  username: 'alice',
  displayName: 'Alice',
  email: 'alice@example.com',
  emailVerified: true,
  passwordSet: true,
  passkeyCount: 0,
  oauthProviders: [] as { id: string, name: string, enabled: boolean }[],
}

describe('accountLinkedProviders', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mockUser.mockReturnValue(base)
    mockRefreshUser.mockResolvedValue(undefined)
    mockUnlink.mockResolvedValue({})
  })

  // The row used to vanish for an account with no link, which left the reader
  // unable to tell "I have none" from "this panel is broken".
  it('says so when the account signs in through no provider', async () => {
    render(() => <AccountLinkedProviders />)
    expect(await screen.findByText(/signs in with no identity provider/)).toBeInTheDocument()
  })

  it('detaches a linked provider', async () => {
    mockUser.mockReturnValue({ ...base, oauthProviders: [{ id: 'github-1', name: 'GitHub', enabled: true }] })
    render(() => <AccountLinkedProviders />)
    expect(await screen.findByText('GitHub')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Unlink' }))
    await vi.waitFor(() => {
      expect(mockUnlink).toHaveBeenCalledWith({ providerId: 'github-1' })
    })
  })

  // A link whose provider an administrator DISABLED is still listed, and still
  // detachable.
  //
  // Filtering it out was the regression: the list is the only feed for this
  // row, so the row vanished and the Unlink button with it -- while
  // UnlinkOAuthProvider's own last-login-method guard still counted the link,
  // leaving the owner holding a login method they could neither use nor
  // remove.
  it('lists a disabled provider so the owner can still detach it', async () => {
    mockUser.mockReturnValue({ ...base, oauthProviders: [{ id: 'github-1', name: 'GitHub', enabled: false }] })
    render(() => <AccountLinkedProviders />)
    expect(await screen.findByText('GitHub')).toBeInTheDocument()

    // And it SAYS it is disabled. Listing it without that note makes it
    // indistinguishable from a working link, so the user tries to sign in with
    // it and every OAuth leg answers a bare 403 outside the app.
    expect(screen.getByTestId('linked-disabled-github-1')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Unlink' }))
    await vi.waitFor(() => {
      expect(mockUnlink).toHaveBeenCalledWith({ providerId: 'github-1' })
    })
  })

  it('does not label an enabled provider as disabled', async () => {
    mockUser.mockReturnValue({ ...base, oauthProviders: [{ id: 'github-1', name: 'GitHub', enabled: true }] })
    render(() => <AccountLinkedProviders />)
    expect(await screen.findByText('GitHub')).toBeInTheDocument()
    expect(screen.queryByTestId('linked-disabled-github-1')).not.toBeInTheDocument()
  })

  it('reports a refusal a prompt cannot fix instead of opening one', async () => {
    // FailedPrecondition WITHOUT the elevation marker: the last login method,
    // which no amount of verifying will admit. Opening the prompt here would
    // ask the user for a factor and then refuse them anyway.
    mockUser.mockReturnValue({ ...base, oauthProviders: [{ id: 'github-1', name: 'GitHub', enabled: true }] })
    mockUnlink.mockRejectedValue(
      new ConnectError('cannot detach your only login method; set a password first', Code.FailedPrecondition),
    )

    render(() => <AccountLinkedProviders />)
    fireEvent.click(await screen.findByRole('button', { name: 'Unlink' }))

    expect(await screen.findByText(/only login method/)).toBeInTheDocument()
    expect(screen.queryByTestId('elevate-password')).not.toBeInTheDocument()
    expect(mockUnlink).toHaveBeenCalledTimes(1)
  })

  it('lists every link', async () => {
    mockUser.mockReturnValue({
      ...base,
      oauthProviders: [
        { id: 'github-1', name: 'GitHub', enabled: true },
        { id: 'oidc-1', name: 'Corp SSO', enabled: true },
      ],
    })
    render(() => <AccountLinkedProviders />)
    expect(await screen.findByText('GitHub')).toBeInTheDocument()
    expect(screen.getByText('Corp SSO')).toBeInTheDocument()
    expect(screen.getAllByRole('button', { name: 'Unlink' })).toHaveLength(2)
  })
})
