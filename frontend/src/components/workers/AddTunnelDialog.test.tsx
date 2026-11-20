/// <reference types="vitest/globals" />
import type { TunnelStore } from '~/stores/tunnel.store'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it, vi } from 'vitest'
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

// The identity scope the form captures and the retirement it reacts to. The
// signal lives in the mock so the dialog's retirement effect tracks it.
const authState = vi.hoisted(() => ({
  setUserId: undefined as undefined | ((id: string | undefined) => void),
}))
vi.mock('~/context/AuthContext', async () => {
  const { createSignal } = await import('solid-js')
  const [user, setUser] = createSignal<{ id: string } | null>({ id: 'user-1' })
  authState.setUserId = id => setUser(id === undefined
    ? null
    : { id })
  return {
    useAuth: () => ({ user }),
  }
})

// The shared TOFU pin the create admits against. A full stand-in: the real
// module reaches account-scoped storage, which this dialog's tests do not
// exercise. `pin: null` is a CONFIRMED first contact; a rejection is a failed
// read, which the dialog must not read as one.
const pinState = vi.hoisted(() => ({
  readWorkerKeyPin: vi.fn<() => Promise<{ pin: { publicKeyHex: string, firstSeen: number } | null, removalRevision: number }>>(),
  notifyKeyPinsChanged: undefined as undefined | (() => void),
}))
vi.mock('~/lib/keyPinStore', () => ({
  readWorkerKeyPin: () => pinState.readWorkerKeyPin(),
  onKeyPinsChanged: (listener: () => void) => {
    pinState.notifyKeyPinsChanged = listener
    return () => {}
  },
  clearKeyPin: vi.fn(),
  clearAllKeyPins: vi.fn(),
  KeyPinRejectedError: class KeyPinRejectedError extends Error {},
  KeyPinStore: class KeyPinStore {},
}))

const verifyWorkerKey = vi.hoisted(() => vi.fn<(workerId: string) => Promise<void>>())
vi.mock('./verifyWorkerKey', () => ({
  verifyWorkerKey: (workerId: string) => verifyWorkerKey(workerId),
}))

function createMockTunnelStore(overrides?: Partial<TunnelStore>): TunnelStore {
  return {
    tunnels: () => [],
    tunnelsForWorker: () => [],
    channelEpoch: () => 'epoch-1',
    refresh: vi.fn().mockResolvedValue(undefined),
    add: vi.fn().mockResolvedValue({
      kind: 'created',
      info: {
        id: 't1',
        workerId: 'w1',
        type: 'port_forward',
        bindAddr: '127.0.0.1',
        bindPort: 3000,
        targetAddr: '127.0.0.1',
        targetPort: 3000,
      },
    }),
    remove: vi.fn().mockResolvedValue(undefined),
    removeAllForWorker: vi.fn().mockResolvedValue(undefined),
    ...overrides,
  }
}

function renderDialog(overrides?: {
  store?: Partial<TunnelStore>
  onClose?: () => void
  onCreated?: (t: unknown) => void
}) {
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

function fillPortForward() {
  fireEvent.input(screen.getByTestId('target-port'), {
    target: { value: '3000' },
  })
}

function submit() {
  fireEvent.submit(screen.getByTestId('tunnel-create').closest('form')!)
}

/**
 * The trust state is loaded, not read in the render — the pin store may answer
 * through a strict transaction. Wait for it before asserting on the Create
 * button, which is disabled while the expectation is unknown.
 */
async function awaitTrustState() {
  await screen.findByTestId('tunnel-trusted-key')
}

beforeEach(() => {
  authState.setUserId?.('user-1')
  pinState.readWorkerKeyPin.mockResolvedValue({
    pin: { publicKeyHex: 'aabb', firstSeen: 1000 },
    removalRevision: 0,
  })
  verifyWorkerKey.mockReset()
  verifyWorkerKey.mockResolvedValue(undefined)
})

describe('AddTunnelDialog', () => {
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
    fireEvent.input(targetPort, {
      target: { value: '0' },
    })
    expect(screen.getByText('Must be 1-65535')).toBeInTheDocument()
  })

  it('port validation rejects 65536', () => {
    renderDialog()
    const targetPort = screen.getByTestId('target-port')
    fireEvent.input(targetPort, {
      target: { value: '65536' },
    })
    expect(screen.getByText('Must be 1-65535')).toBeInTheDocument()
  })

  it('port validation rejects non-numeric', () => {
    renderDialog()
    const targetPort = screen.getByTestId('target-port')
    fireEvent.input(targetPort, {
      target: { value: 'abc' },
    })
    expect(screen.getByText('Must be 1-65535')).toBeInTheDocument()
  })

  it('port validation accepts valid ports', () => {
    renderDialog()
    const targetPort = screen.getByTestId('target-port')

    for (const port of ['1', '80', '443', '65535']) {
      fireEvent.input(targetPort, {
        target: { value: port },
      })
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
      fireEvent.input(screen.getByTestId('target-port'), {
        target: { value },
      })
      expect(screen.getByText('Must be 1-65535')).toBeInTheDocument()
    })
  }

  it('port validation accepts a decimal port padded with spaces', () => {
    renderDialog()
    fireEvent.input(screen.getByTestId('target-port'), {
      target: { value: ' 80 ' },
    })
    expect(screen.queryByText('Must be 1-65535')).not.toBeInTheDocument()
  })

  it('bind port validation rejects a hex literal too', () => {
    renderDialog()
    fireEvent.input(screen.getByTestId('bind-port'), {
      target: { value: '0x50' },
    })
    expect(screen.getByText('Must be 1-65535')).toBeInTheDocument()
  })

  it('does not submit a port the user did not type', () => {
    const { store } = renderDialog()
    fireEvent.input(screen.getByTestId('target-port'), {
      target: { value: '0x50' },
    })
    expect(screen.getByTestId('tunnel-create')).toBeDisabled()
    expect(store.add).not.toHaveBeenCalled()
  })

  it('address validation accepts hostname', () => {
    renderDialog()
    const targetAddr = screen.getByTestId('target-addr')
    fireEvent.input(targetAddr, {
      target: { value: 'my-host.local' },
    })
    expect(screen.queryByText('Required')).not.toBeInTheDocument()
  })

  it('address validation accepts IPv4', () => {
    renderDialog()
    const targetAddr = screen.getByTestId('target-addr')
    fireEvent.input(targetAddr, {
      target: { value: '192.168.1.1' },
    })
    expect(screen.queryByText('Required')).not.toBeInTheDocument()
  })

  it('address validation rejects empty target address', () => {
    renderDialog()
    const targetAddr = screen.getByTestId('target-addr')
    fireEvent.input(targetAddr, {
      target: { value: '' },
    })
    expect(screen.getByText('Required')).toBeInTheDocument()
  })

  it('create button disabled when target port empty in port_forward mode', () => {
    renderDialog()
    const createBtn = screen.getByTestId('tunnel-create')
    expect(createBtn).toBeDisabled()
  })

  it('create button enabled when all fields filled', async () => {
    renderDialog()
    await awaitTrustState()
    fillPortForward()
    const createBtn = screen.getByTestId('tunnel-create')
    expect(createBtn).not.toBeDisabled()
  })

  it('submit calls tunnelStore.add with correct config', async () => {
    const { store } = renderDialog()
    await awaitTrustState()
    fillPortForward()
    submit()

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

    await awaitTrustState()
    fillPortForward()
    submit()

    await vi.waitFor(() => {
      expect(screen.getByText('Port already in use')).toBeInTheDocument()
    })
    expect(onCreated).not.toHaveBeenCalled()
  })

  it('submit success closes dialog', async () => {
    const onCreated = vi.fn()
    renderDialog({ onCreated })
    await awaitTrustState()
    fillPortForward()
    submit()

    await vi.waitFor(() => {
      expect(onCreated).toHaveBeenCalled()
    })
  })

  it('bind port defaults to target port for port forwarding', async () => {
    const { store } = renderDialog()
    await awaitTrustState()
    fillPortForward()
    submit()

    await vi.waitFor(() => {
      expect(store.add).toHaveBeenCalledWith(expect.objectContaining({
        bindPort: 3000,
      }))
    })
  })

  it('bind port defaults to 1080 for SOCKS5', async () => {
    const { store } = renderDialog()
    await awaitTrustState()
    fireEvent.click(screen.getByDisplayValue('socks5'))
    submit()

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

describe('AddTunnelDialog bind address validation', () => {
  // The parser itself (Go-compatible net.ParseIP mirroring, `::` compression,
  // IPv4-mapped loopback) is tested at its own level in ~/lib/ipAddress.test.ts. What
  // the dialog owns is wiring it to the field and surfacing the message.
  //
  // Neither tunnel listener authenticates, so a non-loopback bind address would
  // expose an open gateway into the worker's network to the whole LAN. The sidecar
  // refuses it; the dialog must say so up front rather than let the create fail.
  it('rejects a bind address that is not loopback', () => {
    renderDialog()
    fireEvent.input(screen.getByTestId('bind-addr'), {
      target: { value: '0.0.0.0' },
    })
    expect(screen.getByText(/loopback/i)).toBeInTheDocument()
  })

  it('rejects a bind address that is not an IP address at all', () => {
    renderDialog()
    fireEvent.input(screen.getByTestId('bind-addr'), {
      target: { value: 'localhost' },
    })
    expect(screen.getByText(/loopback/i)).toBeInTheDocument()
  })

  it('accepts a loopback bind address', () => {
    renderDialog()
    fireEvent.input(screen.getByTestId('bind-addr'), {
      target: { value: '127.0.0.1' },
    })
    expect(screen.queryByText(/loopback/i)).not.toBeInTheDocument()
  })

  it('requires a bind address', () => {
    renderDialog()
    fireEvent.input(screen.getByTestId('bind-addr'), {
      target: { value: '' },
    })
    expect(screen.getByText('Required')).toBeInTheDocument()
  })

  // The bracket pair is stripped for validation, so it must be stripped from the
  // SUBMITTED value too: the sidecar's net.ParseIP("[::1]") returns nil.
  it('accepts a bracketed IPv6 loopback and submits it unbracketed', async () => {
    const { store } = renderDialog()
    fireEvent.input(screen.getByTestId('bind-addr'), {
      target: { value: '[::1]' },
    })
    expect(screen.queryByText(/loopback/i)).not.toBeInTheDocument()

    await awaitTrustState()
    fireEvent.input(screen.getByTestId('target-port'), {
      target: { value: '3000' },
    })
    submit()

    await vi.waitFor(() => {
      expect(store.add).toHaveBeenCalledWith(expect.objectContaining({ bindAddr: '::1' }))
    })
  })
})

describe('AddTunnelDialog key trust', () => {
  // The three admission fields a create carries: the exact Worker key set, the
  // user that expectation belongs to, and the channel epoch the form was
  // opened on. The sidecar refuses a create that omits any of them, so each is
  // asserted here rather than inferred from a passing create.
  it('submits the pinned Worker key, the captured identity, and the channel epoch', async () => {
    const { store } = renderDialog()
    await awaitTrustState()
    fillPortForward()
    submit()

    await vi.waitFor(() => {
      expect(store.add).toHaveBeenCalledWith(expect.objectContaining({
        expectedWorkerKeysHex: 'aabb',
        expectedUserId: 'user-1',
        channelEpoch: 'epoch-1',
      }))
    })
  })

  it('refuses to create with no trusted Worker key and points at the verify button', async () => {
    pinState.readWorkerKeyPin.mockResolvedValue({ pin: null, removalRevision: 0 })
    const { store } = renderDialog()
    fillPortForward()

    await vi.waitFor(() => {
      expect(screen.getByTestId('tunnel-no-trusted-key')).toBeInTheDocument()
    })
    expect(screen.getByTestId('tunnel-create')).toBeDisabled()
    submit()
    expect(store.add).not.toHaveBeenCalled()
  })

  // A failed read is not a first contact. Reporting it as "no trusted key"
  // would point the user at the verify button and turn a storage failure into
  // a silent trust decision.
  it('reports a failed key read as a failure, not as an unpinned Worker', async () => {
    pinState.readWorkerKeyPin.mockRejectedValue(new Error('storage is unavailable'))
    const { store } = renderDialog()
    fillPortForward()

    await vi.waitFor(() => {
      expect(screen.getByTestId('tunnel-key-read-error')).toHaveTextContent('storage is unavailable')
    })
    expect(screen.queryByTestId('tunnel-no-trusted-key')).not.toBeInTheDocument()
    expect(screen.getByTestId('tunnel-create')).toBeDisabled()
    submit()
    expect(store.add).not.toHaveBeenCalled()
  })

  // The trust state is a view over durable pin state. A change another tab
  // committed moves it too, not only this form's own verification.
  it('follows a pin change another tab committed', async () => {
    pinState.readWorkerKeyPin.mockResolvedValue({ pin: null, removalRevision: 0 })
    const { store } = renderDialog()
    fillPortForward()
    await vi.waitFor(() => {
      expect(screen.getByTestId('tunnel-no-trusted-key')).toBeInTheDocument()
    })

    pinState.readWorkerKeyPin.mockResolvedValue({
      pin: { publicKeyHex: 'eeff', firstSeen: 3000 },
      removalRevision: 1,
    })
    pinState.notifyKeyPinsChanged?.()

    await vi.waitFor(() => {
      expect(screen.getByTestId('tunnel-create')).not.toBeDisabled()
    })
    submit()
    await vi.waitFor(() => {
      expect(store.add).toHaveBeenCalledWith(expect.objectContaining({
        expectedWorkerKeysHex: 'eeff',
      }))
    })
  })

  it('offers to verify the Worker key through the browser channel path', async () => {
    renderDialog()
    await vi.waitFor(() => {
      expect(screen.getByTestId('tunnel-verify-key')).toBeInTheDocument()
    })
    fireEvent.click(screen.getByTestId('tunnel-verify-key'))

    await vi.waitFor(() => {
      expect(verifyWorkerKey).toHaveBeenCalledWith('w1')
    })
  })

  // The verify path is what records a key in the shared pin store, so the
  // create after it must admit against the key it left behind rather than the
  // one the form opened with.
  it('re-reads the pinned key after a verification', async () => {
    const { store } = renderDialog()
    pinState.readWorkerKeyPin.mockResolvedValue({ pin: null, removalRevision: 0 })
    fillPortForward()

    await vi.waitFor(() => {
      expect(screen.getByTestId('tunnel-no-trusted-key')).toBeInTheDocument()
    })
    pinState.readWorkerKeyPin.mockResolvedValue({
      pin: { publicKeyHex: 'ccdd', firstSeen: 2000 },
      removalRevision: 1,
    })
    fireEvent.click(screen.getByTestId('tunnel-verify-key'))

    await vi.waitFor(() => {
      expect(screen.getByTestId('tunnel-create')).not.toBeDisabled()
    })
    submit()

    await vi.waitFor(() => {
      expect(store.add).toHaveBeenCalledWith(expect.objectContaining({
        expectedWorkerKeysHex: 'ccdd',
      }))
    })
  })

  it('shows the expected and presented fingerprints when the create is refused with a key mismatch', async () => {
    const { store, onCreated } = renderDialog({
      store: {
        add: vi.fn().mockResolvedValue({
          kind: 'workerKeyMismatch',
          keyMismatch: {
            expectedFingerprint: 'deep-idea-obey-tack',
            presentedFingerprint: 'calm-lion-fern-arch',
          },
        }),
      },
    })
    await awaitTrustState()
    fillPortForward()
    submit()

    await vi.waitFor(() => {
      expect(screen.getByTestId('tunnel-key-mismatch')).toBeInTheDocument()
    })
    expect(screen.getByTestId('tunnel-expected-fingerprint')).toHaveTextContent('deep-idea-obey-tack')
    expect(screen.getByTestId('tunnel-presented-fingerprint')).toHaveTextContent('calm-lion-fern-arch')
    expect(onCreated).not.toHaveBeenCalled()
    expect(store.add).toHaveBeenCalledTimes(1)
  })

  it('offers the verify button beside a refused key mismatch', async () => {
    renderDialog({
      store: {
        add: vi.fn().mockResolvedValue({
          kind: 'workerKeyMismatch',
          keyMismatch: {
            expectedFingerprint: 'deep-idea-obey-tack',
            presentedFingerprint: 'calm-lion-fern-arch',
          },
        }),
      },
    })
    await awaitTrustState()
    fillPortForward()
    submit()

    await vi.waitFor(() => {
      expect(screen.getByTestId('tunnel-key-mismatch')).toBeInTheDocument()
    })
    fireEvent.click(screen.getByTestId('tunnel-verify-key'))
    await vi.waitFor(() => {
      expect(verifyWorkerKey).toHaveBeenCalledWith('w1')
    })
  })

  it('shows an actionable error inline, announced as an alert, when the create fails', async () => {
    renderDialog({
      store: {
        add: vi.fn().mockRejectedValue(new Error('127.0.0.1:3000: address already in use')),
      },
    })
    await awaitTrustState()
    fillPortForward()
    submit()

    await vi.waitFor(() => {
      expect(screen.getByTestId('tunnel-create-error')).toHaveTextContent('address already in use')
    })
    expect(screen.getByTestId('tunnel-create-error')).toHaveAttribute('role', 'alert')
  })
})

describe('AddTunnelDialog identity scope', () => {
  it('dismisses when the identity retires', async () => {
    const onClose = vi.fn()
    renderDialog({ onClose })
    expect(onClose).not.toHaveBeenCalled()

    authState.setUserId?.('user-2')
    await vi.waitFor(() => {
      expect(onClose).toHaveBeenCalled()
    })
  })

  it('dismisses on sign-out', async () => {
    const onClose = vi.fn()
    renderDialog({ onClose })

    authState.setUserId?.(undefined)
    await vi.waitFor(() => {
      expect(onClose).toHaveBeenCalled()
    })
  })

  it('rejects a submission made after the identity changed', async () => {
    const { store, onClose } = renderDialog()
    fillPortForward()
    authState.setUserId?.('user-2')
    await vi.waitFor(() => {
      expect(onClose).toHaveBeenCalled()
    })

    submit()
    // The rejection is silent: the form is already on its way out, and a
    // request must not leave on behalf of a retired identity.
    await Promise.resolve()
    expect(store.add).not.toHaveBeenCalled()
  })

  // A reply that lands after the identity retired describes a tunnel for an
  // identity this document no longer holds. Nothing of it may land, and the
  // reply must not report the create as done.
  it('does not apply a create reply that lands after the identity retired', async () => {
    let resolveCreate: (value: unknown) => void = () => {}
    const add = vi.fn().mockImplementation(() => new Promise((resolve) => {
      resolveCreate = resolve
    }))
    const { onCreated, onClose } = renderDialog({
      store: { add },
    })
    await awaitTrustState()
    fillPortForward()
    submit()

    await vi.waitFor(() => {
      expect(add).toHaveBeenCalled()
    })
    authState.setUserId?.('user-2')
    resolveCreate({
      kind: 'created',
      info: {
        id: 't1',
        workerId: 'w1',
        type: 'port_forward',
        bindAddr: '127.0.0.1',
        bindPort: 3000,
        targetAddr: '127.0.0.1',
        targetPort: 3000,
      },
    })

    await vi.waitFor(() => {
      expect(onClose).toHaveBeenCalled()
    })
    expect(onCreated).not.toHaveBeenCalled()
  })
})
