import { timestampFromDate } from '@bufbuild/protobuf/wkt'
import { fireEvent, render, screen, within } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { AccountConnectedApps } from './AccountConnectedApps'

const mockList = vi.fn()
const mockRevoke = vi.fn()
const mockDisconnect = vi.fn()

vi.mock('~/api/clients', () => ({
  userClient: {
    listMyAPITokens: (...args: unknown[]) => mockList(...args),
    revokeMyAPIToken: (...args: unknown[]) => mockRevoke(...args),
    disconnectApp: (...args: unknown[]) => mockDisconnect(...args),
  },
}))

/**
 * One credential of the built-in control CLI, on one machine.
 *
 * `clientName` is the APP and `installationName` is the machine — two fields,
 * because one app holds one credential per machine and the panel's whole shape
 * rests on telling them apart.
 */
const laptop = {
  id: 'tok-1',
  clientId: 'leapmux-control-cli',
  clientName: 'LeapMux control CLI',
  clientVerified: true,
  installationName: 'alice@laptop',
  createdAt: timestampFromDate(new Date('2026-01-01T00:00:00Z')),
  refreshExpiresAt: timestampFromDate(new Date('2026-04-01T00:00:00Z')),
  grantedScopes: ['workspace:read'],
  current: false,
}

/** A second installation of the SAME app. */
const desktop = { ...laptop, id: 'tok-2', installationName: 'alice@desktop' }

/** A different app entirely, so a cascade's bound is observable. */
const otherApp = {
  ...laptop,
  id: 'tok-3',
  clientId: 'app-deploy-bot',
  clientName: 'Deployment bot',
  clientVerified: false,
  installationName: 'ci-runner',
}

/**
 * Arm the confirmation dialog's danger button, which takes two clicks: the
 * first arms it, the second confirms.
 */
async function confirmDialog(name: string, action: string): Promise<void> {
  const dialog = await screen.findByRole('dialog', { name })
  fireEvent.click(within(dialog).getByRole('button', { name: action }))
  fireEvent.click(await within(dialog).findByRole('button', { name: 'Confirm?' }))
}

describe('accountConnectedApps', () => {
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
    mockDisconnect.mockResolvedValue({ revokedCredentialCount: 1n })
  })

  it('lists the app and the machine it runs on', async () => {
    render(() => <AccountConnectedApps />)
    expect(await screen.findByText('LeapMux control CLI')).toBeInTheDocument()
    expect(screen.getByText('alice@laptop')).toBeInTheDocument()
  })

  // The grouping is the panel's shape: one block per APP, one row per machine
  // inside it. A flat credential list made "stop this app reaching my account"
  // a repeated action whose completeness the reader had to verify by eye.
  it('groups the installations of one app under it', async () => {
    mockList.mockResolvedValue({ tokens: [laptop, desktop, otherApp] })
    render(() => <AccountConnectedApps />)

    const cli = await screen.findByTestId('connected-app-leapmux-control-cli')
    expect(within(cli).getByTestId('app-credential-tok-1')).toBeInTheDocument()
    expect(within(cli).getByTestId('app-credential-tok-2')).toBeInTheDocument()
    // And the other app's credential is NOT in this block, which is what makes
    // the grouping a claim rather than a coincidence of ordering.
    expect(within(cli).queryByTestId('app-credential-tok-3')).not.toBeInTheDocument()

    const bot = screen.getByTestId('connected-app-app-deploy-bot')
    expect(within(bot).getByTestId('app-credential-tok-3')).toBeInTheDocument()
  })

  // Grouped on client_id, NEVER on the name. The name is the registrant's
  // choice and two apps may share one, so grouping by it would merge two apps
  // into a block whose single Disconnect took only one of them.
  it('keeps two apps apart when they chose the same name', async () => {
    mockList.mockResolvedValue({
      tokens: [laptop, { ...otherApp, clientName: 'LeapMux control CLI' }],
    })
    render(() => <AccountConnectedApps />)

    expect(await screen.findByTestId('connected-app-leapmux-control-cli')).toBeInTheDocument()
    expect(screen.getByTestId('connected-app-app-deploy-bot')).toBeInTheDocument()
  })

  // An unverified app is labelled here exactly as it is on the consent screen:
  // nobody vouched for it, and that is the one signal a person has.
  it('labels an app no administrator vouched for', async () => {
    mockList.mockResolvedValue({ tokens: [laptop, otherApp] })
    render(() => <AccountConnectedApps />)

    const bot = await screen.findByTestId('connected-app-app-deploy-bot')
    expect(within(bot).getByText('unverified')).toBeInTheDocument()
    const cli = screen.getByTestId('connected-app-leapmux-control-cli')
    expect(within(cli).queryByText('unverified')).not.toBeInTheDocument()
  })

  // The warning is read from the GRANT itself, not from a separate flag, so
  // the badge and the permission list below it can never disagree.
  it('says which credentials administer the hub', async () => {
    mockList.mockResolvedValue({
      tokens: [
        { ...laptop, id: 'tok-2', installationName: 'ci-bot', grantedScopes: ['admin:read'] },
        { ...laptop, id: 'tok-3', installationName: 'plain-bot', grantedScopes: ['workspace:read'] },
      ],
    })
    render(() => <AccountConnectedApps />)
    expect(await screen.findByText('ci-bot')).toBeInTheDocument()
    expect(screen.getByText('hub administration')).toBeInTheDocument()

    // Exactly ONE row carries it. Without this the assertion above would pass
    // for a badge rendered on every row.
    expect(screen.getAllByText('hub administration')).toHaveLength(1)
  })

  // The permission list is the point of this panel: it is what a person reads
  // to decide whether to disconnect, so every granted scope renders. A count
  // would answer a question nobody asked.
  it('lists every permission a credential holds', async () => {
    mockList.mockResolvedValue({
      tokens: [{ ...laptop, grantedScopes: ['workspace:read', 'file:read', 'git:write'] }],
    })
    render(() => <AccountConnectedApps />)
    const scopes = await screen.findByTestId('app-scopes-tok-1')
    expect(scopes).toHaveTextContent('workspace:read')
    expect(scopes).toHaveTextContent('file:read')
    expect(scopes).toHaveTextContent('git:write')
  })

  // An empty grant renders NOTHING rather than "none". The hub refuses to
  // store one, so a row without permissions is a credential this panel could
  // not read, and a confident "none" would be a lie.
  it('renders no permission list for a grant it cannot read', async () => {
    mockList.mockResolvedValue({ tokens: [{ ...laptop, grantedScopes: [] }] })
    render(() => <AccountConnectedApps />)
    expect(await screen.findByText('alice@laptop')).toBeInTheDocument()
    expect(screen.queryByTestId('app-scopes-tok-1')).not.toBeInTheDocument()
  })

  // MyAPIToken.current marks the credential that made the CALL, and the hub
  // derives it from the caller's own credential -- so it is false for every
  // row a browser session sees, always. This page renders nothing for it,
  // and the assertion is that it renders nothing even when a response says
  // otherwise: a "(this device)" label here could only ever come from a
  // response the hub cannot produce.
  it('never marks a listed credential as this device', async () => {
    mockList.mockResolvedValue({ tokens: [{ ...laptop, current: true }] })
    render(() => <AccountConnectedApps />)
    expect(await screen.findByText('alice@laptop')).toBeInTheDocument()
    expect(screen.queryByText('(this device)')).not.toBeInTheDocument()
  })

  it('reports an empty account', async () => {
    mockList.mockResolvedValue({ tokens: [] })
    render(() => <AccountConnectedApps />)
    expect(await screen.findByText(/No connected apps/)).toBeInTheDocument()
  })

  // The verb the whole panel exists for. It takes EVERY machine the app runs
  // on, in ONE call: a client loop over the installations would report success
  // after a partial failure and leave the app working somewhere the reader
  // believes is disconnected.
  it('disconnects every installation of an app in one call', async () => {
    mockList.mockResolvedValue({ tokens: [laptop, desktop, otherApp] })
    mockDisconnect.mockResolvedValue({ revokedCredentialCount: 2n })
    render(() => <AccountConnectedApps />)

    const cli = await screen.findByTestId('connected-app-leapmux-control-cli')
    fireEvent.click(within(cli).getByRole('button', { name: 'Disconnect' }))
    await confirmDialog('Disconnect this app?', 'Disconnect')

    await vi.waitFor(() => {
      expect(mockDisconnect).toHaveBeenCalledWith({ clientId: 'leapmux-control-cli' })
    })
    expect(mockRevoke).not.toHaveBeenCalled()
    await vi.waitFor(() => {
      expect(screen.queryByTestId('connected-app-leapmux-control-cli')).not.toBeInTheDocument()
    })
    // BOTH installations go, and the other app stays.
    expect(screen.queryByTestId('app-credential-tok-1')).not.toBeInTheDocument()
    expect(screen.queryByTestId('app-credential-tok-2')).not.toBeInTheDocument()
    expect(screen.getByTestId('connected-app-app-deploy-bot')).toBeInTheDocument()
    expect(screen.getByText('App disconnected.')).toBeInTheDocument()
  })

  // The confirmation states the COUNT, because this ending takes every machine
  // and a reader with one installation in front of them has no other way to
  // tell whether it takes the others.
  it('says how many machines a disconnect covers', async () => {
    mockList.mockResolvedValue({ tokens: [laptop, desktop] })
    render(() => <AccountConnectedApps />)

    const cli = await screen.findByTestId('connected-app-leapmux-control-cli')
    fireEvent.click(within(cli).getByRole('button', { name: 'Disconnect' }))
    const dialog = await screen.findByRole('dialog', { name: 'Disconnect this app?' })
    expect(dialog).toHaveTextContent('all 2 machines')
  })

  // Revoking ends ONE installation and leaves the app connected everywhere
  // else, which is how a single laptop is signed out.
  it('revokes one installation and keeps the app connected', async () => {
    mockList.mockResolvedValue({ tokens: [laptop, desktop] })
    render(() => <AccountConnectedApps />)

    const row = await screen.findByTestId('app-credential-tok-1')
    fireEvent.click(within(row).getByRole('button', { name: 'Revoke' }))
    await confirmDialog('Revoke this credential?', 'Revoke')

    await vi.waitFor(() => {
      expect(mockRevoke).toHaveBeenCalledWith({ id: 'tok-1' })
    })
    expect(mockDisconnect).not.toHaveBeenCalled()
    await vi.waitFor(() => {
      expect(screen.queryByTestId('app-credential-tok-1')).not.toBeInTheDocument()
    })
    expect(screen.getByTestId('app-credential-tok-2')).toBeInTheDocument()
    expect(screen.getByTestId('connected-app-leapmux-control-cli')).toBeInTheDocument()
    expect(screen.getByText('Credential revoked.')).toBeInTheDocument()
  })

  // The app block disappears when its LAST installation goes, because the
  // grouping is derived from the credential list rather than stored beside it.
  it('drops the app when its last installation is revoked', async () => {
    render(() => <AccountConnectedApps />)

    const row = await screen.findByTestId('app-credential-tok-1')
    fireEvent.click(within(row).getByRole('button', { name: 'Revoke' }))
    await confirmDialog('Revoke this credential?', 'Revoke')

    await vi.waitFor(() => {
      expect(screen.queryByTestId('connected-app-leapmux-control-cli')).not.toBeInTheDocument()
    })
  })

  // The same rule in the confirmation: a browser is never one of the
  // credentials it lists, so there is no "you are revoking yourself" case to
  // warn about here. `leapmux control auth credentials` is the caller that
  // can be one, and it reports `current` in its own output.
  it('does not warn about revoking itself', async () => {
    mockList.mockResolvedValue({ tokens: [{ ...laptop, current: true }] })
    render(() => <AccountConnectedApps />)
    const row = await screen.findByTestId('app-credential-tok-1')
    fireEvent.click(within(row).getByRole('button', { name: 'Revoke' }))
    await screen.findByRole('dialog', { name: 'Revoke this credential?' })
    expect(screen.queryByText(/credential making this request/)).not.toBeInTheDocument()
  })

  it('keeps the app when the disconnect fails', async () => {
    // The page shows the hub's own message verbatim (formatErrorMessage
    // prefers it over the fallback), so this asserts on what the user sees.
    mockDisconnect.mockRejectedValue(new Error('the hub refused'))
    render(() => <AccountConnectedApps />)
    const cli = await screen.findByTestId('connected-app-leapmux-control-cli')
    fireEvent.click(within(cli).getByRole('button', { name: 'Disconnect' }))
    await confirmDialog('Disconnect this app?', 'Disconnect')
    await vi.waitFor(() => {
      expect(screen.getByText('the hub refused')).toBeInTheDocument()
    })
    expect(screen.getByTestId('connected-app-leapmux-control-cli')).toBeInTheDocument()
  })

  it('keeps the row when the revoke fails', async () => {
    mockRevoke.mockRejectedValue(new Error('the hub refused'))
    render(() => <AccountConnectedApps />)
    const row = await screen.findByTestId('app-credential-tok-1')
    fireEvent.click(within(row).getByRole('button', { name: 'Revoke' }))
    await confirmDialog('Revoke this credential?', 'Revoke')
    await vi.waitFor(() => {
      expect(screen.getByText('the hub refused')).toBeInTheDocument()
    })
    expect(screen.getByTestId('app-credential-tok-1')).toBeInTheDocument()
  })

  // The listing is keyset-paginated and an omitted limit resolves to fifty,
  // so reading one page silently truncated the list -- and either ending is
  // only reachable from a rendered row, so a row the page never drew was a
  // credential the owner could not end here at all.
  it('reads every page, so a credential past the first is revocable', async () => {
    mockList
      .mockResolvedValueOnce({ tokens: [laptop], nextCursor: 'page-2' })
      .mockResolvedValueOnce({ tokens: [desktop], nextCursor: '' })

    render(() => <AccountConnectedApps />)

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

    render(() => <AccountConnectedApps />)

    expect(await screen.findByText('alice@laptop')).toBeInTheDocument()
    expect(mockList).toHaveBeenCalledTimes(2)
  })

  // A failed load must not also claim the account owns nothing. This is the
  // one screen the docs point somebody at when they believe a credential is
  // stolen, and "No connected apps" is the worst possible answer to give them
  // when the truth is that the hub did not reply.
  it('does not claim the account owns none when the load failed', async () => {
    mockList.mockRejectedValue(new Error('the hub is unreachable'))

    render(() => <AccountConnectedApps />)

    expect(await screen.findByText('the hub is unreachable')).toBeInTheDocument()
    expect(screen.queryByText(/No connected apps/)).not.toBeInTheDocument()
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
describe('accountConnectedApps deadlines', () => {
  const renewing = timestampFromDate(new Date('2026-04-01T00:00:00Z'))
  const fixed = timestampFromDate(new Date('2027-02-15T00:00:00Z'))

  /** The whole meta line of one installation row, as the reader sees it. */
  async function metaOf(tokenId: string): Promise<string> {
    const row = await screen.findByTestId(`app-credential-${tokenId}`)
    return row.textContent ?? ''
  }

  beforeEach(() => {
    vi.clearAllMocks()
    mockRevoke.mockResolvedValue({})
    mockDisconnect.mockResolvedValue({ revokedCredentialCount: 1n })
  })

  it('reports when a renewing credential signs in again', async () => {
    mockList.mockResolvedValue({ tokens: [{ ...laptop, refreshExpiresAt: renewing }] })
    render(() => <AccountConnectedApps />)

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
    render(() => <AccountConnectedApps />)

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
    render(() => <AccountConnectedApps />)

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
    render(() => <AccountConnectedApps />)

    const meta = await metaOf('tok-1')
    expect(meta).toContain('Added')
    expect(meta).not.toContain('Expires')
    expect(meta).not.toContain('Signs in again')
  })
})
