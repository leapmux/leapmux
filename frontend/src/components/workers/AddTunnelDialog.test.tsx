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
        hubURL="http://localhost:4327"
        token="tok"
        userId="u1"
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
        hubURL: 'http://localhost:4327',
        token: 'tok',
        userId: 'u1',
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
          hubURL="http://localhost:4327"
          token="tok"
          userId="u1"
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
