import { timestampFromDate } from '@bufbuild/protobuf/wkt'
import { fireEvent, render, screen, within } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { AccountCLITokens } from './AccountCLITokens'

const mockList = vi.fn()
const mockRevoke = vi.fn()

vi.mock('~/api/clients', () => ({
  userClient: {
    listMyAPITokens: (...args: unknown[]) => mockList(...args),
    revokeMyAPIToken: (...args: unknown[]) => mockRevoke(...args),
  },
}))

const laptop = {
  id: 'tok-1',
  clientType: 'cli',
  clientName: 'alice@laptop',
  createdAt: timestampFromDate(new Date('2026-01-01T00:00:00Z')),
  refreshExpiresAt: timestampFromDate(new Date('2026-04-01T00:00:00Z')),
  adminScope: false,
  current: false,
}

describe('accountCLITokens', () => {
  beforeEach(() => {
    HTMLDialogElement.prototype.showModal = vi.fn(function (this: HTMLDialogElement) {
      this.open = true
    })
    HTMLDialogElement.prototype.close = vi.fn(function (this: HTMLDialogElement) {
      this.open = false
    })
    vi.clearAllMocks()
    mockList.mockResolvedValue({ tokens: [laptop] })
    mockRevoke.mockResolvedValue({})
  })

  it('lists the account credentials', async () => {
    render(() => <AccountCLITokens />)
    expect(await screen.findByText('alice@laptop')).toBeInTheDocument()
  })

  it('says which credentials administer the hub', async () => {
    mockList.mockResolvedValue({
      tokens: [
        { ...laptop, id: 'tok-2', clientName: 'ci-bot', adminScope: true },
        { ...laptop, id: 'tok-3', clientName: 'plain-bot' },
      ],
    })
    render(() => <AccountCLITokens />)
    expect(await screen.findByText('ci-bot')).toBeInTheDocument()
    expect(screen.getByText('(hub administration)')).toBeInTheDocument()
  })

  // MyAPIToken.current marks the credential that made the CALL, and the hub
  // derives it from the caller's own credential -- so it is false for every
  // row a browser session sees, always. This page renders nothing for it,
  // and the assertion is that it renders nothing even when a response says
  // otherwise: a "(this device)" label here could only ever come from a
  // response the hub cannot produce.
  it('never marks a listed credential as this device', async () => {
    mockList.mockResolvedValue({ tokens: [{ ...laptop, current: true }] })
    render(() => <AccountCLITokens />)
    expect(await screen.findByText('alice@laptop')).toBeInTheDocument()
    expect(screen.queryByText('(this device)')).not.toBeInTheDocument()
  })

  it('reports an empty account', async () => {
    mockList.mockResolvedValue({ tokens: [] })
    render(() => <AccountCLITokens />)
    expect(await screen.findByText(/No command-line credentials/)).toBeInTheDocument()
  })

  it('revokes after a confirmation and drops the row', async () => {
    render(() => <AccountCLITokens />)
    expect(await screen.findByText('alice@laptop')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Revoke' }))

    const dialog = await screen.findByRole('dialog', { name: 'Revoke this credential?' })
    // The primary action is a ConfirmButton (danger): the first click arms
    // it, the second confirms.
    fireEvent.click(within(dialog).getByRole('button', { name: 'Revoke' }))
    fireEvent.click(await within(dialog).findByRole('button', { name: 'Confirm?' }))
    await vi.waitFor(() => {
      expect(mockRevoke).toHaveBeenCalledWith({ id: 'tok-1' })
    })
    await vi.waitFor(() => {
      expect(screen.queryByText('alice@laptop')).not.toBeInTheDocument()
    })
    expect(screen.getByText('Credential revoked.')).toBeInTheDocument()
  })

  // The same rule in the confirmation: a browser is never one of the
  // credentials it lists, so there is no "you are revoking yourself" case to
  // warn about here. `leapmux control auth credentials` is the caller that
  // can be one, and it reports `current` in its own output.
  it('does not warn about revoking itself', async () => {
    mockList.mockResolvedValue({ tokens: [{ ...laptop, current: true }] })
    render(() => <AccountCLITokens />)
    expect(await screen.findByText('alice@laptop')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Revoke' }))
    await screen.findByRole('dialog', { name: 'Revoke this credential?' })
    expect(screen.queryByText(/credential making this request/)).not.toBeInTheDocument()
  })

  it('keeps the row when the revoke fails', async () => {
    // The page shows the hub's own message verbatim (formatErrorMessage
    // prefers it over the fallback), so this asserts on what the user sees.
    mockRevoke.mockRejectedValue(new Error('the hub refused'))
    render(() => <AccountCLITokens />)
    expect(await screen.findByText('alice@laptop')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Revoke' }))
    const dialog = await screen.findByRole('dialog', { name: 'Revoke this credential?' })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Revoke' }))
    fireEvent.click(await within(dialog).findByRole('button', { name: 'Confirm?' }))
    await vi.waitFor(() => {
      expect(screen.getByText('the hub refused')).toBeInTheDocument()
    })
    expect(screen.getByText('alice@laptop')).toBeInTheDocument()
  })

  // The listing is keyset-paginated and an omitted limit resolves to fifty,
  // so reading one page silently truncated the list -- and revoking is only
  // reachable from a rendered row, so a row the page never drew was a
  // credential the owner could not revoke here at all.
  it('reads every page, so a credential past the first is revocable', async () => {
    const desktop = { ...laptop, id: 'tok-2', clientName: 'alice@desktop' }
    mockList
      .mockResolvedValueOnce({ tokens: [laptop], nextCursor: 'page-2' })
      .mockResolvedValueOnce({ tokens: [desktop], nextCursor: '' })

    render(() => <AccountCLITokens />)

    expect(await screen.findByText('alice@desktop')).toBeInTheDocument()
    expect(screen.getByText('alice@laptop')).toBeInTheDocument()
    // The limit is asked for, not left to the hub's default of fifty: the
    // page-count limit below is sized on the maximum, and an omitted limit made
    // the loop cover a tenth of what it claims at ten times the cost.
    expect(mockList).toHaveBeenNthCalledWith(1, { cursor: '', limit: 500n })
    expect(mockList).toHaveBeenNthCalledWith(2, { cursor: 'page-2', limit: 500n })
  })

  // A client loop whose only exit is a value the server chooses is a hang
  // the server can cause. A cursor that never advances stops it.
  it('stops when the cursor does not advance', async () => {
    mockList.mockResolvedValue({ tokens: [laptop], nextCursor: 'stuck' })

    render(() => <AccountCLITokens />)

    expect(await screen.findByText('alice@laptop')).toBeInTheDocument()
    expect(mockList).toHaveBeenCalledTimes(2)
  })

  // A failed load must not also claim the account owns nothing. This is the
  // one screen the docs point somebody at when they believe a credential is
  // stolen, and "No command-line credentials" is the worst possible answer
  // to give them when the truth is that the hub did not reply.
  it('does not claim the account owns none when the load failed', async () => {
    mockList.mockRejectedValue(new Error('the hub is unreachable'))

    render(() => <AccountCLITokens />)

    expect(await screen.findByText('the hub is unreachable')).toBeInTheDocument()
    expect(screen.queryByText(/No command-line credentials/)).not.toBeInTheDocument()
  })
})

/**
 * The deadline each row reports.
 *
 * The hub sets exactly ONE of the two fields, and the two kinds are exclusive.
 * A renewing credential carries `refresh_expires_at` and answers "when do I
 * sign in again"; a credential minted with an explicit lifetime carries
 * `expires_at` and answers "when do I stop working". Before the second case
 * had a line of its own, a one-year service credential and one that never
 * expires read exactly alike on this page.
 */
describe('accountCLITokens deadlines', () => {
  const renewing = timestampFromDate(new Date('2026-04-01T00:00:00Z'))
  const fixed = timestampFromDate(new Date('2027-02-15T00:00:00Z'))

  /** The whole meta line of one row, as the reader sees it. */
  async function metaOf(tokenId: string): Promise<string> {
    const row = await screen.findByTestId(`cli-token-${tokenId}`)
    return row.textContent ?? ''
  }

  beforeEach(() => {
    vi.clearAllMocks()
    mockRevoke.mockResolvedValue({})
  })

  it('reports when a renewing credential signs in again', async () => {
    mockList.mockResolvedValue({ tokens: [{ ...laptop, refreshExpiresAt: renewing }] })
    render(() => <AccountCLITokens />)

    const meta = await metaOf('tok-1')
    expect(meta).toContain(`Signs in again ${new Date('2026-04-01T00:00:00Z').toLocaleDateString()}`)
    expect(meta).not.toContain('Expires')
  })

  // The kind that carries no refresh deadline: `admin api-token issue --ttl`.
  // Its whole life is the only deadline it has, so the row states that instead.
  it('reports when a fixed-lifetime credential stops working', async () => {
    mockList.mockResolvedValue({
      tokens: [{ ...laptop, refreshExpiresAt: undefined, expiresAt: fixed }],
    })
    render(() => <AccountCLITokens />)

    const meta = await metaOf('tok-1')
    expect(meta).toContain(`Expires ${new Date('2027-02-15T00:00:00Z').toLocaleDateString()}`)
    expect(meta).not.toContain('Signs in again')
  })

  // Both fields present is a response the hub does not produce. The renewing
  // deadline wins, because an access expiry that moves at every rotation reads
  // as "expiring today" on a credential with months left.
  it('states one deadline, never two', async () => {
    mockList.mockResolvedValue({
      tokens: [{ ...laptop, refreshExpiresAt: renewing, expiresAt: fixed }],
    })
    render(() => <AccountCLITokens />)

    const meta = await metaOf('tok-1')
    expect(meta).toContain('Signs in again')
    expect(meta).not.toContain('Expires')
  })

  // Neither field is a credential that never expires, so the row states no
  // deadline at all rather than an empty or a fabricated one.
  it('states no deadline for a credential that never expires', async () => {
    mockList.mockResolvedValue({
      tokens: [{ ...laptop, refreshExpiresAt: undefined, expiresAt: undefined }],
    })
    render(() => <AccountCLITokens />)

    const meta = await metaOf('tok-1')
    expect(meta).toContain('Added')
    expect(meta).not.toContain('Expires')
    expect(meta).not.toContain('Signs in again')
  })
})
