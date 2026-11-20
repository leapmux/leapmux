/// <reference types="vitest/globals" />
import type { AddTunnelTarget } from './AddTunnelDialog'
import type { TunnelKeyApproval } from '~/api/workerRpc'
import type { WorkerTrustScope } from '~/context/AuthContext'
import type { TunnelStore } from '~/stores/tunnel.store'
import { create } from '@bufbuild/protobuf'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { TunnelKeyApprovalExpiredError } from '~/api/workerRpc'
import { TunnelProvider } from '~/context/TunnelContext'
import { WorkerSchema } from '~/generated/proto/leapmux/v1/worker_pb'
import { deferred, settle } from '~/test-support/async'
import { testTunnelKeyApproval as approve } from '~/test-support/workerTrust'
import { AddTunnelDialog } from './AddTunnelDialog'

beforeAll(() => {
  HTMLDialogElement.prototype.showModal = vi.fn(function (this: HTMLDialogElement) {
    this.setAttribute('open', '')
  })
  HTMLDialogElement.prototype.close = vi.fn(function (this: HTMLDialogElement) {
    this.removeAttribute('open')
  })
})

const authState = vi.hoisted(() => ({
  setUserId: undefined as undefined | ((id: string | undefined) => void),
  scope: undefined as undefined | (() => WorkerTrustScope),
}))
vi.mock('~/context/AuthContext', async () => {
  const { createTestWorkerTrustState } = await import('~/test-support/workerTrust')
  const state = createTestWorkerTrustState()
  authState.scope = state.scope
  authState.setUserId = state.setUserId
  return {
    useAuth: () => ({ workerTrustScope: state.scope }),
  }
})

const verifyWorkerKey = vi.hoisted(() => vi.fn<(workerId: string, scope: WorkerTrustScope, signal?: AbortSignal) => Promise<TunnelKeyApproval>>())
vi.mock('./verifyWorkerKey', () => ({ verifyWorkerKey }))

function createMockTunnelStore(overrides?: Partial<TunnelStore>): TunnelStore {
  return {
    tunnels: () => [],
    tunnelsForWorker: () => [],
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
  replacement?: AddTunnelTarget['replacement']
}) {
  const store = createMockTunnelStore(overrides?.store)
  const onClose = overrides?.onClose ?? vi.fn()
  const onCreated = overrides?.onCreated ?? vi.fn()
  const target = {
    worker: create(WorkerSchema, { id: 'w1' }),
    scope: authState.scope!(),
    ...(overrides?.replacement
      ? { replacement: overrides.replacement }
      : {}),
  }

  const { unmount } = render(() => (
    <TunnelProvider store={store}>
      <AddTunnelDialog
        target={target}
        onClose={onClose}
        onCreated={onCreated}
      />
    </TunnelProvider>
  ))

  return {
    store,
    onClose,
    onCreated,
    target,
    unmount,
  }
}

function fillPortForward() {
  fireEvent.input(screen.getByTestId('target-port'), {
    target: { value: '3000' },
  })
}

function submit() {
  fireEvent.submit(screen.getByTestId('tunnel-create').closest('form')!)
}

beforeEach(() => {
  authState.setUserId?.('user-1')
  verifyWorkerKey.mockReset()
  verifyWorkerKey.mockImplementation(async (_worker, scope) => approve(scope))
})

afterEach(() => vi.useRealTimers())

describe('AddTunnelDialog', () => {
  it('retires an open form when a new lifetime adopts the same user', () => {
    const { onClose } = renderDialog()

    authState.setUserId?.('user-1')

    expect(onClose).toHaveBeenCalledOnce()
  })

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

  it('enables Create when all fields are filled', () => {
    renderDialog()
    fillPortForward()
    const createBtn = screen.getByTestId('tunnel-create')
    expect(createBtn).not.toBeDisabled()
  })

  it('submit calls tunnelStore.add with correct config', async () => {
    const { store } = renderDialog()
    fillPortForward()
    submit()

    await vi.waitFor(() => {
      expect(store.add).toHaveBeenCalledWith(expect.objectContaining({
        workerId: 'w1',
        type: 'port_forward',
        targetAddr: '127.0.0.1',
        targetPort: 3000,
        bindAddr: '127.0.0.1',
      }), authState.scope!(), undefined)
    })
  })

  it('keeps the dialog open when submission fails', async () => {
    const { onCreated } = renderDialog({
      store: {
        add: vi.fn().mockRejectedValue(new Error('Port already in use')),
      },
    })
    fillPortForward()
    submit()

    expect(await screen.findByText('Port already in use')).toBeInTheDocument()
    expect(onCreated).not.toHaveBeenCalled()
  })

  it('submit success closes dialog', async () => {
    const onCreated = vi.fn()
    renderDialog({ onCreated })
    fillPortForward()
    submit()

    await vi.waitFor(() => {
      expect(onCreated).toHaveBeenCalled()
    })
  })

  it('bind port defaults to target port for port forwarding', async () => {
    const { store } = renderDialog()
    fillPortForward()
    submit()

    await vi.waitFor(() => {
      expect(store.add).toHaveBeenCalledWith(expect.objectContaining({
        bindPort: 3000,
      }), authState.scope!(), undefined)
    })
  })

  it('bind port defaults to 1080 for SOCKS5', async () => {
    const { store } = renderDialog()
    fireEvent.click(screen.getByDisplayValue('socks5'))
    submit()

    await vi.waitFor(() => {
      expect(store.add).toHaveBeenCalledWith(expect.objectContaining({
        bindPort: 1080,
      }), authState.scope!(), undefined)
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

    fireEvent.input(screen.getByTestId('target-port'), {
      target: { value: '3000' },
    })
    submit()

    await vi.waitFor(() => {
      expect(store.add).toHaveBeenCalledWith(expect.objectContaining({ bindAddr: '::1' }), authState.scope!(), undefined)
    })
  })
})

describe('AddTunnelDialog key trust', () => {
  const mismatch = {
    kind: 'workerKeyMismatch',
    keyMismatch: {
      expectedFingerprint: 'deep-idea-obey-tack',
      presentedFingerprint: 'calm-lion-fern-arch',
    },
  } as const

  it('keeps the original fields and requires an explicit Create after fresh verification', async () => {
    vi.useFakeTimers()
    const add = vi.fn().mockResolvedValue(mismatch)
    const { onCreated, target } = renderDialog({
      store: { add },
    })
    fillPortForward()
    submit()

    await vi.runOnlyPendingTimersAsync()
    expect(screen.getByTestId('tunnel-key-mismatch')).toHaveTextContent('The Hub reported a different Worker key.')
    expect(screen.getByTestId('tunnel-expected-fingerprint')).toHaveTextContent('deep-idea-obey-tack')
    expect(screen.getByTestId('tunnel-presented-fingerprint')).toHaveTextContent('calm-lion-fern-arch')
    expect(onCreated).not.toHaveBeenCalled()
    expect(screen.getByTestId('tunnel-create')).toBeDisabled()
    fireEvent.click(screen.getByTestId('tunnel-verify-key'))
    await vi.advanceTimersByTimeAsync(0)
    expect(screen.getByTestId('tunnel-create')).toBeEnabled()
    expect(verifyWorkerKey).toHaveBeenCalledExactlyOnceWith('w1', target.scope, expect.any(AbortSignal))
    expect(add).toHaveBeenCalledOnce()
    submit()
    await vi.advanceTimersByTimeAsync(0)
    expect(add).toHaveBeenCalledTimes(2)
    expect(add).toHaveBeenLastCalledWith(expect.objectContaining({ targetPort: 3000 }), target.scope, approve(target.scope))
  })

  it('prefills the replacement and never submits an expired approval as first contact', async () => {
    const scope = authState.scope!()
    const replacement = {
      config: {
        workerId: 'w1',
        type: 'port_forward' as const,
        targetAddr: 'internal.example',
        targetPort: 8443,
        bindAddr: '::1',
        bindPort: 9443,
      },
      approval: approve(scope),
    }
    const add = vi.fn().mockRejectedValue(new TunnelKeyApprovalExpiredError())
    renderDialog({
      replacement,
      store: { add },
    })

    expect(screen.getByTestId('target-addr')).toHaveValue('internal.example')
    expect(screen.getByTestId('target-port')).toHaveValue('8443')
    expect(screen.getByTestId('bind-addr')).toHaveValue('::1')
    expect(screen.getByTestId('bind-port')).toHaveValue('9443')
    expect(add).not.toHaveBeenCalled()

    submit()
    expect(await screen.findByTestId('tunnel-create-error')).toHaveTextContent('Verify the Worker key again.')
    expect(add).toHaveBeenCalledExactlyOnceWith(replacement.config, scope, replacement.approval)
    expect(screen.getByTestId('tunnel-create')).toBeDisabled()
    submit()
    expect(add).toHaveBeenCalledOnce()
  })

  it('aborts a pending review when the form closes', async () => {
    const scope = authState.scope!()
    const review = deferred<TunnelKeyApproval>()
    let signal: AbortSignal | undefined
    verifyWorkerKey.mockImplementationOnce((_workerId, _scope, operationSignal) => {
      signal = operationSignal
      return review.promise
    })
    const rendered = renderDialog({
      replacement: {
        config: {
          workerId: 'w1',
          type: 'port_forward',
          targetAddr: 'internal.example',
          targetPort: 8443,
          bindAddr: '127.0.0.1',
          bindPort: 9443,
        },
        approval: approve(scope),
      },
      store: {
        add: vi.fn().mockRejectedValue(new TunnelKeyApprovalExpiredError()),
      },
    })
    submit()
    fireEvent.click(await screen.findByTestId('tunnel-verify-key'))
    expect(signal?.aborted).toBe(false)

    rendered.unmount()
    expect(signal?.aborted).toBe(true)
    review.resolve(approve(scope))
    await settle()
    expect(rendered.onCreated).not.toHaveBeenCalled()
  })

  it('discards the values of a retired replacement before a new form opens', () => {
    const scope = authState.scope!()
    const previous = renderDialog({
      replacement: {
        config: {
          workerId: 'w1',
          type: 'socks5',
          targetAddr: '',
          targetPort: 0,
          bindAddr: '::1',
          bindPort: 9876,
        },
        approval: approve(scope),
      },
    })
    expect(screen.getByTestId('bind-port')).toHaveValue('9876')

    authState.setUserId?.('user-1')
    expect(previous.onClose).toHaveBeenCalledOnce()
    previous.unmount()
    renderDialog()
    expect(screen.getByDisplayValue('port_forward')).toBeChecked()
    expect(screen.getByTestId('bind-addr')).toHaveValue('127.0.0.1')
    expect(screen.getByTestId('bind-port')).toHaveValue('')
  })

  it('discards a late verification after the account changes', async () => {
    const verification = deferred<TunnelKeyApproval>()
    verifyWorkerKey.mockReturnValueOnce(verification.promise)
    const add = vi.fn().mockResolvedValue(mismatch)
    const { onClose, target } = renderDialog({
      store: { add },
    })
    fillPortForward()
    submit()

    fireEvent.click(await screen.findByTestId('tunnel-verify-key'))
    expect(verifyWorkerKey).toHaveBeenCalledExactlyOnceWith('w1', target.scope, expect.any(AbortSignal))
    authState.setUserId?.('user-2')
    verification.resolve(approve(target.scope))
    await settle()

    expect(onClose).toHaveBeenCalledOnce()
    expect(add).toHaveBeenCalledOnce()
    expect(screen.getByTestId('tunnel-create')).toBeDisabled()
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
