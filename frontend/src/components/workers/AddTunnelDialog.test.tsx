/// <reference types="vitest/globals" />
import type { TunnelStore } from '~/stores/tunnel.store'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { beforeAll, describe, expect, it, vi } from 'vitest'
import { TunnelProvider } from '~/context/TunnelContext'
import { AddTunnelDialog } from './AddTunnelDialog'

beforeAll(() => {
  HTMLDialogElement.prototype.showModal = vi.fn(function (this: HTMLDialogElement) {
    this.setAttribute('open', '')
  })
  HTMLDialogElement.prototype.close = vi.fn(function (this: HTMLDialogElement) {
    this.removeAttribute('open')
  })
})

function createMockTunnelStore(overrides?: Partial<TunnelStore>): TunnelStore {
  return {
    tunnels: () => [],
    tunnelsForWorker: () => [],
    refresh: vi.fn().mockResolvedValue(undefined),
    add: vi.fn().mockResolvedValue({
      id: 't1',
      workerId: 'w1',
      type: 'port_forward',
      bindAddr: '127.0.0.1',
      bindPort: 3000,
      targetAddr: '127.0.0.1',
      targetPort: 3000,
    }),
    remove: vi.fn().mockResolvedValue(undefined),
    removeAllForWorker: vi.fn().mockResolvedValue(undefined),
    ...overrides,
  }
}

function renderDialog(overrides?: { store?: Partial<TunnelStore>, onClose?: () => void, onCreated?: () => void }) {
  const store = createMockTunnelStore(overrides?.store)
  const onClose = overrides?.onClose ?? vi.fn()
  const onCreated = overrides?.onCreated ?? vi.fn()

  render(() => (
    <TunnelProvider store={store}>
      <AddTunnelDialog
        workerId="w1"
        onClose={onClose}
        onCreated={onCreated}
      />
    </TunnelProvider>
  ))

  return { store, onClose, onCreated }
}

describe('addTunnelDialog', () => {
  it('renders with port forwarding selected by default', () => {
    renderDialog()
    const portFwdRadio = screen.getByDisplayValue('port_forward') as HTMLInputElement
    expect(portFwdRadio.checked).toBe(true)
    expect(screen.getByTestId('target-addr')).toBeInTheDocument()
    expect(screen.getByTestId('target-port')).toBeInTheDocument()
    expect(screen.getByTestId('bind-addr')).toBeInTheDocument()
    expect(screen.getByTestId('bind-port')).toBeInTheDocument()
  })

  it('switching to SOCKS5 hides target fields', () => {
    renderDialog()
    const socks5Radio = screen.getByDisplayValue('socks5')
    fireEvent.click(socks5Radio)
    expect(screen.queryByTestId('target-addr')).not.toBeInTheDocument()
    expect(screen.queryByTestId('target-port')).not.toBeInTheDocument()
    expect(screen.getByTestId('bind-addr')).toBeInTheDocument()
    expect(screen.getByTestId('bind-port')).toBeInTheDocument()
  })

  it('switching back to port forwarding restores target fields', () => {
    renderDialog()
    fireEvent.click(screen.getByDisplayValue('socks5'))
    expect(screen.queryByTestId('target-addr')).not.toBeInTheDocument()

    fireEvent.click(screen.getByDisplayValue('port_forward'))
    expect(screen.getByTestId('target-addr')).toBeInTheDocument()
    expect(screen.getByTestId('target-port')).toBeInTheDocument()
  })

  it('port validation rejects 0', () => {
    renderDialog()
    const targetPort = screen.getByTestId('target-port')
    fireEvent.input(targetPort, { target: { value: '0' } })
    expect(screen.getByText('Must be 1-65535')).toBeInTheDocument()
  })

  it('port validation rejects 65536', () => {
    renderDialog()
    const targetPort = screen.getByTestId('target-port')
    fireEvent.input(targetPort, { target: { value: '65536' } })
    expect(screen.getByText('Must be 1-65535')).toBeInTheDocument()
  })

  it('port validation rejects non-numeric', () => {
    renderDialog()
    const targetPort = screen.getByTestId('target-port')
    fireEvent.input(targetPort, { target: { value: 'abc' } })
    expect(screen.getByText('Must be 1-65535')).toBeInTheDocument()
  })

  it('port validation accepts valid ports', () => {
    renderDialog()
    const targetPort = screen.getByTestId('target-port')

    for (const port of ['1', '80', '443', '65535']) {
      fireEvent.input(targetPort, { target: { value: port } })
      expect(screen.queryByText('Must be 1-65535')).not.toBeInTheDocument()
    }
  })

  // Both port fields are free text: `inputMode="numeric"` is a keyboard hint, not
  // validation. Number() converts every one of these to an in-range port, so accepting
  // them would silently create a tunnel on a port the user never typed -- and the
  // sidecar's range check cannot catch it, because the substituted port is in range.
  const nonDecimalPorts = [
    ['0x50', 'hex literal Number() reads as 80'],
    ['0b101', 'binary literal Number() reads as 5'],
    ['0o17', 'octal literal Number() reads as 15'],
    ['1e3', 'exponent form Number() reads as 1000'],
    ['80.0', 'float spelling Number() reads as 80'],
    ['+80', 'signed form Number() reads as 80'],
    ['0xFFFF', 'hex literal Number() reads as 65535'],
  ] as const
  for (const [value, why] of nonDecimalPorts) {
    it(`port validation rejects ${JSON.stringify(value)} (${why})`, () => {
      renderDialog()
      fireEvent.input(screen.getByTestId('target-port'), { target: { value } })
      expect(screen.getByText('Must be 1-65535')).toBeInTheDocument()
    })
  }

  it('port validation accepts a decimal port padded with spaces', () => {
    renderDialog()
    fireEvent.input(screen.getByTestId('target-port'), { target: { value: ' 80 ' } })
    expect(screen.queryByText('Must be 1-65535')).not.toBeInTheDocument()
  })

  it('bind port validation rejects a hex literal too', () => {
    renderDialog()
    fireEvent.input(screen.getByTestId('bind-port'), { target: { value: '0x50' } })
    expect(screen.getByText('Must be 1-65535')).toBeInTheDocument()
  })

  it('does not submit a port the user did not type', () => {
    const { store } = renderDialog()
    fireEvent.input(screen.getByTestId('target-port'), { target: { value: '0x50' } })
    expect(screen.getByTestId('tunnel-create')).toBeDisabled()
    expect(store.add).not.toHaveBeenCalled()
  })

  it('address validation accepts hostname', () => {
    renderDialog()
    const targetAddr = screen.getByTestId('target-addr')
    fireEvent.input(targetAddr, { target: { value: 'my-host.local' } })
    expect(screen.queryByText('Required')).not.toBeInTheDocument()
  })

  it('address validation accepts IPv4', () => {
    renderDialog()
    const targetAddr = screen.getByTestId('target-addr')
    fireEvent.input(targetAddr, { target: { value: '192.168.1.1' } })
    expect(screen.queryByText('Required')).not.toBeInTheDocument()
  })

  it('address validation rejects empty target address', () => {
    renderDialog()
    const targetAddr = screen.getByTestId('target-addr')
    fireEvent.input(targetAddr, { target: { value: '' } })
    expect(screen.getByText('Required')).toBeInTheDocument()
  })

  it('create button disabled when target port empty in port_forward mode', () => {
    renderDialog()
    const createBtn = screen.getByTestId('tunnel-create')
    expect(createBtn).toBeDisabled()
  })

  it('create button enabled when all fields filled', () => {
    renderDialog()
    const targetPort = screen.getByTestId('target-port')
    fireEvent.input(targetPort, { target: { value: '3000' } })
    const createBtn = screen.getByTestId('tunnel-create')
    expect(createBtn).not.toBeDisabled()
  })

  it('submit calls tunnelStore.add with correct config', async () => {
    const { store } = renderDialog()
    const targetPort = screen.getByTestId('target-port')
    fireEvent.input(targetPort, { target: { value: '3000' } })

    const form = screen.getByTestId('tunnel-create').closest('form')!
    fireEvent.submit(form)

    await vi.waitFor(() => {
      expect(store.add).toHaveBeenCalledWith(expect.objectContaining({
        workerId: 'w1',
        type: 'port_forward',
        targetAddr: '127.0.0.1',
        targetPort: 3000,
        bindAddr: '127.0.0.1',
      }))
    })
  })

  it('submit error keeps dialog open', async () => {
    const store = createMockTunnelStore({
      add: vi.fn().mockRejectedValue(new Error('Port already in use')),
    })
    const onCreated = vi.fn()

    render(() => (
      <TunnelProvider store={store}>
        <AddTunnelDialog
          workerId="w1"
          onClose={() => {}}
          onCreated={onCreated}
        />
      </TunnelProvider>
    ))

    const targetPort = screen.getByTestId('target-port')
    fireEvent.input(targetPort, { target: { value: '3000' } })
    const form = screen.getByTestId('tunnel-create').closest('form')!
    fireEvent.submit(form)

    await vi.waitFor(() => {
      expect(screen.getByText('Port already in use')).toBeInTheDocument()
    })
    expect(onCreated).not.toHaveBeenCalled()
  })

  it('submit success closes dialog', async () => {
    const onCreated = vi.fn()
    renderDialog({ onCreated })

    const targetPort = screen.getByTestId('target-port')
    fireEvent.input(targetPort, { target: { value: '3000' } })
    const form = screen.getByTestId('tunnel-create').closest('form')!
    fireEvent.submit(form)

    await vi.waitFor(() => {
      expect(onCreated).toHaveBeenCalled()
    })
  })

  it('bind port defaults to target port for port forwarding', async () => {
    const { store } = renderDialog()
    const targetPort = screen.getByTestId('target-port')
    fireEvent.input(targetPort, { target: { value: '3000' } })

    const form = screen.getByTestId('tunnel-create').closest('form')!
    fireEvent.submit(form)

    await vi.waitFor(() => {
      expect(store.add).toHaveBeenCalledWith(expect.objectContaining({
        bindPort: 3000,
      }))
    })
  })

  it('bind port defaults to 1080 for SOCKS5', async () => {
    const { store } = renderDialog()
    fireEvent.click(screen.getByDisplayValue('socks5'))

    const form = screen.getByTestId('tunnel-create').closest('form')!
    fireEvent.submit(form)

    await vi.waitFor(() => {
      expect(store.add).toHaveBeenCalledWith(expect.objectContaining({
        bindPort: 1080,
      }))
    })
  })

  it('cancel button calls onClose', () => {
    const onClose = vi.fn()
    renderDialog({ onClose })

    const cancelBtn = screen.getByTestId('tunnel-cancel')
    fireEvent.click(cancelBtn)
    expect(onClose).toHaveBeenCalled()
  })
})

describe('addTunnelDialog bind address validation', () => {
  // The parser itself (Go-compatible net.ParseIP mirroring, `::` compression,
  // IPv4-mapped loopback) is tested at its own level in ~/lib/ipAddress.test.ts. What
  // the dialog owns is wiring it to the field and surfacing the message.
  //
  // Neither tunnel listener authenticates, so a non-loopback bind address would
  // expose an open gateway into the worker's network to the whole LAN. The sidecar
  // refuses it; the dialog must say so up front rather than let the create fail.
  it('rejects a bind address that is not loopback', () => {
    renderDialog()
    fireEvent.input(screen.getByTestId('bind-addr'), { target: { value: '0.0.0.0' } })
    expect(screen.getByText(/loopback/i)).toBeInTheDocument()
  })

  it('rejects a bind address that is not an IP address at all', () => {
    renderDialog()
    fireEvent.input(screen.getByTestId('bind-addr'), { target: { value: 'localhost' } })
    expect(screen.getByText(/loopback/i)).toBeInTheDocument()
  })

  it('accepts a loopback bind address', () => {
    renderDialog()
    fireEvent.input(screen.getByTestId('bind-addr'), { target: { value: '127.0.0.1' } })
    expect(screen.queryByText(/loopback/i)).not.toBeInTheDocument()
  })

  it('requires a bind address', () => {
    renderDialog()
    fireEvent.input(screen.getByTestId('bind-addr'), { target: { value: '' } })
    expect(screen.getByText('Required')).toBeInTheDocument()
  })

  // The bracket pair is stripped for validation, so it must be stripped from the
  // SUBMITTED value too: the sidecar's net.ParseIP("[::1]") returns nil.
  it('accepts a bracketed IPv6 loopback and submits it unbracketed', async () => {
    const { store } = renderDialog()
    fireEvent.input(screen.getByTestId('bind-addr'), { target: { value: '[::1]' } })
    expect(screen.queryByText(/loopback/i)).not.toBeInTheDocument()

    fireEvent.input(screen.getByTestId('target-port'), { target: { value: '3000' } })
    fireEvent.submit(screen.getByTestId('tunnel-create').closest('form')!)

    await vi.waitFor(() => {
      expect(store.add).toHaveBeenCalledWith(expect.objectContaining({ bindAddr: '::1' }))
    })
  })
})
