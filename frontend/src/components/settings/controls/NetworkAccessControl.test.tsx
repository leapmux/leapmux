import type { SettingBinding } from '../types'
import { fireEvent, render, screen, within } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { setSystemInfoMock } from '~/test-support/systemInfoMock'
import { NetworkAccessControl } from './NetworkAccessControl'

const mockGetListenStatus = vi.fn()
const mockChangePassword = vi.fn()

vi.mock('~/api/clients', () => ({
  adminNetworkClient: { getListenStatus: (...args: unknown[]) => mockGetListenStatus(...args) },
  userClient: { changePassword: (...args: unknown[]) => mockChangePassword(...args) },
}))

vi.mock('~/lib/systemInfo', async () => {
  const m = await import('~/test-support/systemInfoMock')
  return m.systemInfoMock
})

/** The hub's reply for a loopback-only solo hub on one Ethernet machine. */
function listenStatus(overrides: Record<string, unknown> = {}) {
  return {
    interfaces: [
      {
        name: 'en0',
        up: true,
        addresses: [{ ip: '192.168.1.24', ipv6: false, loopback: false }],
      },
      {
        name: 'lo0',
        up: true,
        addresses: [{ ip: '127.0.0.1', ipv6: false, loopback: true }],
      },
    ],
    defaultAddress: '127.0.0.1:4327',
    configured: [] as string[],
    bound: [{ address: '127.0.0.1:4327', source: 'listen', error: '' }],
    passwordSet: false,
    ...overrides,
  }
}

/** A binding over a mutable stored document, like the admin settings store. */
function fakeBinding(stored: { addresses: string[] } | undefined, set = vi.fn()) {
  return {
    value: () => stored,
    set,
    customized: () => stored !== undefined,
    reset: () => Promise.resolve(),
  } satisfies SettingBinding
}

/** Types a valid password pair into the panel. */
async function fillPassword(pw = 'correct-horse-battery-staple') {
  const newPassword = await screen.findByLabelText('New Password')
  const confirm = screen.getByLabelText('Confirm Password')
  fireEvent.input(newPassword, { target: { value: pw } })
  fireEvent.input(confirm, { target: { value: pw } })
}

describe('networkAccessControl', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    setSystemInfoMock({ soloMode: true, soloPasswordSet: false })
    mockGetListenStatus.mockResolvedValue(listenStatus())
    mockChangePassword.mockResolvedValue({})
  })

  it('reports what the hub serves now, and why', async () => {
    render(() => <NetworkAccessControl binding={fakeBinding(undefined)} />)

    expect(await screen.findByText('127.0.0.1:4327')).toBeInTheDocument()
    expect(screen.getByText('from -listen')).toBeInTheDocument()
  })

  // "127.0.0.1:4327 is gone" and "127.0.0.1:4327 is served by *:4327" look
  // identical in a list that only names what is serving.
  it('says when one socket serves the -listen address too', async () => {
    mockGetListenStatus.mockResolvedValue(listenStatus({
      bound: [{ address: '*:4327', source: 'merged', error: '' }],
    }))
    render(() => <NetworkAccessControl binding={fakeBinding(undefined)} />)

    expect(await screen.findByText('*:4327')).toBeInTheDocument()
    expect(screen.getByText('from -listen, merged')).toBeInTheDocument()
  })

  // A stored address the hub could not bind must be reported against its own
  // address, with the operating system's reason.
  it('reports a bind failure beside its address', async () => {
    mockGetListenStatus.mockResolvedValue(listenStatus({
      bound: [
        { address: '127.0.0.1:4327', source: 'listen', error: '' },
        { address: '192.168.1.24:8080', source: 'extra', error: 'address already in use' },
      ],
    }))
    render(() => <NetworkAccessControl binding={fakeBinding(undefined)} />)

    expect(await screen.findByText('192.168.1.24:8080')).toBeInTheDocument()
    expect(screen.getByText(/bind failed: address already in use/)).toBeInTheDocument()
  })

  // "this hub answers nowhere" and "the hub did not answer" look identical if
  // a failure renders as an empty list, and the first is alarming.
  it('says the hub did not answer rather than showing an empty list', async () => {
    mockGetListenStatus.mockRejectedValue(new Error('boom'))
    render(() => <NetworkAccessControl binding={fakeBinding(undefined)} />)

    expect(await screen.findByText('The hub did not report its listeners.')).toBeInTheDocument()
  })

  it('seeds its rows from the stored addresses', async () => {
    render(() => <NetworkAccessControl binding={fakeBinding({ addresses: ['*:4327', '192.168.1.24:8080'] })} />)

    const rows = await screen.findAllByTestId('network-address-row')
    expect(rows).toHaveLength(2)
    // The TRIGGER, not any text: a DropdownMenu keeps its items mounted whether
    // or not it is open, so "All interfaces" also appears as a menu option.
    expect(within(rows[0]!).getByTestId('network-interface-trigger')).toHaveTextContent('All interfaces')
    expect(within(rows[1]!).getByTestId('network-interface-trigger')).toHaveTextContent('192.168.1.24')
    expect(within(rows[1]!).getByLabelText('Port')).toHaveValue('8080')
  })

  it('adds and removes rows', async () => {
    render(() => <NetworkAccessControl binding={fakeBinding(undefined)} />)
    await screen.findByText('+ Add address')

    fireEvent.click(screen.getByText('+ Add address'))
    expect(screen.getAllByTestId('network-address-row')).toHaveLength(1)
    // Seeded with the port -listen already uses: publishing the hub on the
    // port it already answers on is the common case.
    expect(screen.getByLabelText('Port')).toHaveValue('4327')

    fireEvent.click(screen.getByRole('button', { name: 'Remove *:4327' }))
    expect(screen.queryAllByTestId('network-address-row')).toHaveLength(0)
  })

  // The port is what a client connects to, so a value no client could use must
  // not reach the hub.
  it('refuses to apply a port outside 1-65535', async () => {
    render(() => <NetworkAccessControl binding={fakeBinding({ addresses: ['192.168.1.24:8080'] })} />)
    await fillPassword()
    const port = screen.getByLabelText('Port')

    for (const bad of ['0', '70000', '', 'http']) {
      fireEvent.input(port, { target: { value: bad } })
      expect(screen.getByRole('button', { name: 'Apply' })).toBeDisabled()
    }
    fireEvent.input(port, { target: { value: '8080' } })
    expect(screen.getByRole('button', { name: 'Apply' })).toBeEnabled()
  })

  // Publishing an address without a password would put the whole app behind
  // nothing, so the panel refuses until one exists.
  it('refuses to publish an exposed address while the account has no password', async () => {
    render(() => <NetworkAccessControl binding={fakeBinding({ addresses: ['192.168.1.24:8080'] })} />)

    expect(await screen.findByRole('button', { name: 'Apply' })).toBeDisabled()
    await fillPassword()
    expect(screen.getByRole('button', { name: 'Apply' })).toBeEnabled()
  })

  // The password FIRST: a failure there must leave no address published, where
  // the reverse would publish one nobody can authenticate against.
  it('sets the password before it writes the addresses', async () => {
    const order: string[] = []
    mockChangePassword.mockImplementation(() => {
      order.push('changePassword')
      return Promise.resolve({})
    })
    const set = vi.fn(() => {
      order.push('set')
    })
    render(() => <NetworkAccessControl binding={fakeBinding({ addresses: ['192.168.1.24:8080'] }, set)} />)
    await fillPassword()

    fireEvent.click(screen.getByRole('button', { name: 'Apply' }))

    await vi.waitFor(() => expect(order).toEqual(['changePassword', 'set']))
    expect(mockChangePassword).toHaveBeenCalledWith({ newPassword: 'correct-horse-battery-staple' })
    expect(set).toHaveBeenCalledWith({ addresses: ['192.168.1.24:8080'] })
  })

  // The hub is read back rather than the request trusted: a stored address it
  // could not bind is reported only in the reply.
  it('re-reads the hub after applying', async () => {
    render(() => <NetworkAccessControl binding={fakeBinding({ addresses: ['192.168.1.24:8080'] })} />)
    await fillPassword()
    mockGetListenStatus.mockClear()

    fireEvent.click(screen.getByRole('button', { name: 'Apply' }))

    await vi.waitFor(() => expect(mockGetListenStatus).toHaveBeenCalled())
    expect(await screen.findByText('Network access updated.')).toBeInTheDocument()
  })

  it('reports a refused apply rather than claiming success', async () => {
    const set = vi.fn(() => Promise.reject(new Error('at most 8 extra listen addresses')))
    render(() => <NetworkAccessControl binding={fakeBinding({ addresses: ['192.168.1.24:8080'] }, set)} />)
    await fillPassword()

    fireEvent.click(screen.getByRole('button', { name: 'Apply' }))

    expect(await screen.findByText(/at most 8 extra listen addresses/)).toBeInTheDocument()
    expect(screen.queryByText('Network access updated.')).not.toBeInTheDocument()
  })

  // Once the account holds a password, Preferences → Account owns changing it,
  // so the two surfaces never offer the same field at the same time.
  it('drops its password half once the account has one', async () => {
    setSystemInfoMock({ soloMode: true, soloPasswordSet: true })
    render(() => <NetworkAccessControl binding={fakeBinding({ addresses: ['192.168.1.24:8080'] })} />)

    expect(await screen.findByText(/Change it in Account → Password/)).toBeInTheDocument()
    expect(screen.queryByLabelText('New Password')).not.toBeInTheDocument()
    // And Apply is free: the address is already guarded.
    expect(screen.getByRole('button', { name: 'Apply' })).toBeEnabled()
  })

  // A loopback address exposes nothing, so demanding a password for it would
  // be friction with nothing behind it.
  it('lets a loopback address apply with no password', async () => {
    render(() => <NetworkAccessControl binding={fakeBinding({ addresses: ['127.0.0.1:9000'] })} />)

    expect(await screen.findByRole('button', { name: 'Apply' })).toBeEnabled()
  })

  // Without the note, "Serving now" shows 127.0.0.1:4327 before Apply and only
  // *:4327 after, which reads as an address that disappeared.
  it('states which address a wildcard will absorb', async () => {
    render(() => <NetworkAccessControl binding={fakeBinding({ addresses: ['*:4327'] })} />)

    expect(await screen.findByText('127.0.0.1:4327 merges into *:4327.')).toBeInTheDocument()
  })

  // A closed popover is outside the accessibility tree, so the role queries
  // take `hidden: true` -- the rule ~/test-support/menu.ts states for every
  // DropdownMenu. The ROLE is still asserted: these must be `menuitemradio`,
  // which is what makes the group a one-of-N choice rather than a list of
  // buttons.
  it('offers every interface address and the wildcard', async () => {
    render(() => <NetworkAccessControl binding={fakeBinding({ addresses: ['*:4327'] })} />)
    const row = (await screen.findAllByTestId('network-address-row'))[0]!

    const options = within(row)
      .getAllByRole('menuitemradio', { hidden: true })
      .map(el => el.textContent?.trim())
    expect(options).toEqual(['All interfaces', '192.168.1.24', '127.0.0.1'])
  })

  // The picker groups by interface, so an address is read beside the interface
  // that holds it rather than as a bare literal.
  it('groups the addresses under their interface', async () => {
    render(() => <NetworkAccessControl binding={fakeBinding({ addresses: ['*:4327'] })} />)
    const row = (await screen.findAllByTestId('network-address-row'))[0]!

    expect(within(row).getByText('en0')).toBeInTheDocument()
    expect(within(row).getByText('lo0')).toBeInTheDocument()
  })

  it('returns the rows to the stored list on cancel', async () => {
    render(() => <NetworkAccessControl binding={fakeBinding({ addresses: ['192.168.1.24:8080'] })} />)
    await screen.findByText('+ Add address')

    fireEvent.click(screen.getByText('+ Add address'))
    expect(screen.getAllByTestId('network-address-row')).toHaveLength(2)

    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(screen.getAllByTestId('network-address-row')).toHaveLength(1)
  })
})
