import type { SettingBinding } from '../types'
import { fireEvent, render, screen, within } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { MAX_EXTRA_LISTEN_ADDRESSES } from '~/generated/contracts/listen'
import { mockLoadSystemInfo, setSystemInfoMock } from '~/test-support/systemInfoMock'
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
    // Restated, not merely cleared: `clearAllMocks` forgets the CALLS and
    // keeps the implementation, so one test's `mockRejectedValue` would
    // otherwise reject in every test after it.
    mockLoadSystemInfo.mockResolvedValue(undefined)
  })

  it('reports what the hub serves now, and why', async () => {
    render(() => <NetworkAccessControl binding={fakeBinding(undefined)} />)

    expect(await screen.findByText('127.0.0.1:4327')).toBeInTheDocument()
    expect(screen.getByText('from -listen')).toBeInTheDocument()
  })

  // "127.0.0.1:4327 is gone" and "127.0.0.1:4327 is served by *:4327" look
  // identical in a list that only states what is serving.
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
  //
  // The WARNING has to agree with the button. Telling the operator to set a
  // password first, beside an Apply that never asks for one, is a panel
  // arguing with itself -- and the reader cannot tell which half is wrong.
  it('lets a loopback address apply with no password, and does not demand one', async () => {
    render(() => <NetworkAccessControl binding={fakeBinding({ addresses: ['127.0.0.1:9000'] })} />)

    expect(await screen.findByRole('button', { name: 'Apply' })).toBeEnabled()
    expect(screen.queryByText(/Set the password below first/)).toBeNull()
    // It still states the standing condition: this hub asks nobody for one yet.
    expect(screen.getByText(/authenticates everyone who can reach it/)).toBeInTheDocument()
  })

  // The other half of the same rule: an address another machine can reach is
  // what arms it, so the warning and the button must both hold out for a
  // password.
  it('demands a password before publishing an address beyond loopback', async () => {
    render(() => <NetworkAccessControl binding={fakeBinding({ addresses: ['192.168.1.24:9000'] })} />)

    expect(await screen.findByText(/Set the password below first/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Apply' })).toBeDisabled()
  })

  // Once the account holds one, the warning stops asking for anything and
  // states the rule that is now in force -- including on 127.0.0.1, which is
  // the part an operator does not expect.
  it('states the standing rule once the account holds a password', async () => {
    setSystemInfoMock({ soloMode: true, soloPasswordSet: true })
    render(() => <NetworkAccessControl binding={fakeBinding({ addresses: ['*:4327'] })} />)

    expect(await screen.findByText(/Every network address asks for a sign-in as “solo”, 127\.0\.0\.1 included/))
      .toBeInTheDocument()
    expect(screen.queryByText(/Set the password below first/)).toBeNull()
  })

  // Without the note, "Serving now" shows 127.0.0.1:4327 before Apply and only
  // *:4327 after, which reads as an address that disappeared.
  it('states which address a wildcard will absorb', async () => {
    render(() => <NetworkAccessControl binding={fakeBinding({ addresses: ['*:4327'] })} />)

    expect(await screen.findByText('127.0.0.1:4327 merges into *:4327.')).toBeInTheDocument()
  })

  // The other direction: -listen is an ordinary entry in the merge, so a
  // wildcard THERE absorbs the row. Matching a row's host against `*` reported
  // only the first direction, and this apply showed no note at all before it
  // produced one address where the operator had asked for two.
  it('states when the -listen address absorbs a row', async () => {
    mockGetListenStatus.mockResolvedValue(listenStatus({
      defaultAddress: '*:4327',
      bound: [{ address: '*:4327', source: 'listen', error: '' }],
    }))
    render(() => <NetworkAccessControl binding={fakeBinding({ addresses: ['127.0.0.1:4327'] })} />)

    expect(await screen.findByText('127.0.0.1:4327 merges into *:4327.')).toBeInTheDocument()
  })

  // `0.0.0.0` is a wildcard the interface menu itself can produce, and the
  // host-equals-`*` test called it an ordinary literal.
  it('states the fold into a wildcard that is not spelled with a star', async () => {
    render(() => <NetworkAccessControl binding={fakeBinding({ addresses: ['0.0.0.0:4327'] })} />)

    expect(await screen.findByText('127.0.0.1:4327 merges into 0.0.0.0:4327.')).toBeInTheDocument()
  })

  it('states every fold, not only the first', async () => {
    render(() => <NetworkAccessControl binding={fakeBinding({ addresses: ['*:4327', '192.168.1.24:4327'] })} />)

    expect(await screen.findByText('127.0.0.1:4327 merges into *:4327.')).toBeInTheDocument()
    expect(screen.getByText('192.168.1.24:4327 merges into *:4327.')).toBeInTheDocument()
  })

  it('says nothing about a fold when each address stands on its own', async () => {
    render(() => <NetworkAccessControl binding={fakeBinding({ addresses: ['192.168.1.24:8080'] })} />)

    expect(await screen.findByText(/After apply:/)).toBeInTheDocument()
    expect(screen.queryByText(/merges into/)).toBeNull()
  })

  // A closed popover is outside the accessibility tree, so the role queries
  // take `hidden: true` -- the rule ~/test-support/menu.ts states for every
  // DropdownMenu. The ROLE is still asserted: these must be `menuitemradio`,
  // which is what makes the group a one-of-N choice rather than a list of
  // buttons.
  //
  // The loopback marker is part of the option's NAME, not decoration beside
  // it. Whether the chosen address is loopback decides whether Apply demands a
  // password, so the reason has to reach a screen reader as well as an eye.
  it('offers every interface address and the wildcard, marking the loopback ones', async () => {
    render(() => <NetworkAccessControl binding={fakeBinding({ addresses: ['*:4327'] })} />)
    const row = (await screen.findAllByTestId('network-address-row'))[0]!

    const options = within(row)
      .getAllByRole('menuitemradio', { hidden: true })
      .map(el => el.textContent?.trim())
    expect(options).toEqual(['All interfaces', '192.168.1.24', '127.0.0.1 (loopback)'])
  })

  // The picker groups by interface, so an address is read beside the interface
  // that holds it rather than as a bare literal.
  it('groups the addresses under their interface', async () => {
    render(() => <NetworkAccessControl binding={fakeBinding({ addresses: ['*:4327'] })} />)
    const row = (await screen.findAllByTestId('network-address-row'))[0]!

    expect(within(row).getByText('en0')).toBeInTheDocument()
    expect(within(row).getByText('lo0')).toBeInTheDocument()
  })

  // The shared port validator trims before it tests, so a padded field enables
  // Apply. Sending the padding would reach the hub as a port it refuses with a
  // message about something else.
  it('sends a padded port without its padding', async () => {
    const set = vi.fn()
    render(() => <NetworkAccessControl binding={fakeBinding({ addresses: ['127.0.0.1:9000'] }, set)} />)

    const port = await screen.findByLabelText('Port')
    fireEvent.input(port, { target: { value: ' 9001 ' } })
    expect(screen.getByRole('button', { name: 'Apply' })).toBeEnabled()

    fireEvent.click(screen.getByRole('button', { name: 'Apply' }))
    await vi.waitFor(() => expect(set).toHaveBeenCalledWith({ addresses: ['127.0.0.1:9001'] }))
  })

  // The panel demands a password for what EXPOSES the hub, and it must read
  // the address to know. Matching the row's host against the reported
  // interface list called every loopback address the machine does not hold
  // verbatim exposed -- and, while the status read was in flight or had
  // failed, called EVERY row exposed.
  it('reads loopback from the address, not from the interface list', async () => {
    render(() => <NetworkAccessControl binding={fakeBinding({ addresses: ['127.0.0.5:9000'] })} />)

    // 127.0.0.5 is loopback and this machine does not hold it, so the old
    // lookup answered "exposed" and held Apply out for a password.
    expect(await screen.findByRole('button', { name: 'Apply' })).toBeEnabled()
    expect(screen.queryByText(/Set the password below first/)).toBeNull()
  })

  it('does not demand a password for a loopback list while the hub is unreachable', async () => {
    mockGetListenStatus.mockRejectedValue(new Error('boom'))
    render(() => <NetworkAccessControl binding={fakeBinding({ addresses: ['127.0.0.1:9000'] })} />)

    expect(await screen.findByText('The hub did not report its listeners.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Apply' })).toBeEnabled()
    expect(screen.queryByText(/Set the password below first/)).toBeNull()
  })

  // The hub refuses a ninth address, so the button that builds one must stop
  // at the same number -- otherwise the operator learns the cap from a
  // rejected Apply.
  it('stops offering Add at the hub\'s cap', async () => {
    const at = Array.from({ length: MAX_EXTRA_LISTEN_ADDRESSES }, (_, i) => `127.0.0.1:${9000 + i}`)
    render(() => <NetworkAccessControl binding={fakeBinding({ addresses: at })} />)

    const add = await screen.findByRole('button', { name: '+ Add address' })
    expect(add).toBeDisabled()
    expect(screen.getByText(new RegExp(`At most ${MAX_EXTRA_LISTEN_ADDRESSES} addresses`))).toBeInTheDocument()
  })

  // The password committed, the address write did not. The operator must be
  // able to retry the address without typing a NEW password -- which would
  // silently replace the one they just set.
  it('lets a refused address write be retried after the password landed', async () => {
    const set = vi.fn()
      .mockRejectedValueOnce(new Error('bind refused'))
      .mockResolvedValueOnce(undefined)
    render(() => <NetworkAccessControl binding={fakeBinding({ addresses: ['192.168.1.24:8080'] }, set)} />)
    await fillPassword()

    fireEvent.click(screen.getByRole('button', { name: 'Apply' }))
    expect(await screen.findByText(/bind refused/)).toBeInTheDocument()
    expect(mockChangePassword).toHaveBeenCalledTimes(1)

    // Apply is still offered, and the retry does NOT store the password again.
    expect(screen.getByRole('button', { name: 'Apply' })).toBeEnabled()
    fireEvent.click(screen.getByRole('button', { name: 'Apply' }))
    expect(await screen.findByText('Network access updated.')).toBeInTheDocument()
    expect(mockChangePassword).toHaveBeenCalledTimes(1)
  })

  // Both writes committed, so a failure of the read-back afterwards must not
  // read as a failed apply. loadSystemInfo rejects by design, and the read
  // most likely to fail is the one right after the rule changed.
  it('reports success when the write landed and only the read-back failed', async () => {
    mockLoadSystemInfo.mockRejectedValue(new Error('connection reset'))
    render(() => <NetworkAccessControl binding={fakeBinding({ addresses: ['192.168.1.24:8080'] })} />)
    await fillPassword()

    fireEvent.click(screen.getByRole('button', { name: 'Apply' }))

    expect(await screen.findByText('Network access updated.')).toBeInTheDocument()
    expect(screen.queryByText(/Failed to apply/)).toBeNull()
  })

  // Apply stays disabled through the READ-BACK, not only through the write.
  // The re-read is what removes the password half and lists a bind failure, so
  // an Apply re-enabled before it lands invites a second write against a panel
  // still showing the state from before the first one.
  it('keeps Apply disabled until the read-back lands', async () => {
    let finishReadBack = () => {}
    mockLoadSystemInfo.mockReturnValue(new Promise<void>((resolve) => {
      finishReadBack = resolve
    }))
    // A LOOPBACK address, so nothing but the apply in flight can disable the
    // button: an exposed one re-shows the password half with empty fields, and
    // the assertion below would then pass for the wrong reason.
    render(() => <NetworkAccessControl binding={fakeBinding({ addresses: ['127.0.0.1:9000'] })} />)

    const applyButton = await screen.findByRole('button', { name: 'Apply' })
    fireEvent.click(applyButton)

    const applying = await screen.findByRole('button', { name: 'Applying...' })
    expect(applying).toBeDisabled()

    finishReadBack()
    expect(await screen.findByText('Network access updated.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Apply' })).toBeEnabled()
  })

  // The row above this editor renders a Reset that clears the stored document.
  // Seeding the rows once left the deleted addresses on screen, and the next
  // Apply wrote them straight back.
  it('follows the stored list when the row is reset', async () => {
    const [stored, setStored] = createSignal<{ addresses: string[] } | undefined>({ addresses: ['192.168.1.24:8080'] })
    render(() => (
      <NetworkAccessControl
        binding={{ value: () => stored(), set: vi.fn(), customized: () => stored() !== undefined, reset: () => Promise.resolve() }}
      />
    ))
    expect(await screen.findAllByTestId('network-address-row')).toHaveLength(1)

    setStored(undefined)
    await vi.waitFor(() => expect(screen.queryAllByTestId('network-address-row')).toHaveLength(0))
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
