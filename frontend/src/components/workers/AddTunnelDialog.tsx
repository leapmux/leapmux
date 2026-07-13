import type { Component } from 'solid-js'
import type { TunnelInfo } from '~/api/platformBridge'
import { createMemo, createSignal, Show } from 'solid-js'
import { Dialog } from '~/components/common/Dialog'
import { Spinner } from '~/components/common/Spinner'
import { useTunnel } from '~/context/TunnelContext'
import { useDialogSubmit } from '~/hooks/useDialogSubmit'
import { isLoopbackAddress, normalizeBindAddr } from '~/lib/ipAddress'
import { errorText } from '~/styles/shared.css'
import * as styles from './AddTunnelDialog.css'

interface AddTunnelDialogProps {
  workerId: string
  onClose: () => void
  onCreated: (tunnel: TunnelInfo) => void
}

// Ports are typed as free text (`inputMode="numeric"` is a keyboard hint, not
// validation), so this must reject every non-decimal spelling Number() would happily
// convert: "0x50" -> 80, "1e3" -> 1000, "80.0" -> 80, "+80" -> 80. Accepting those
// silently creates a tunnel on a port the user never typed, and the sidecar's range
// check cannot catch it because the substituted port is in range.
function isValidPort(value: string): boolean {
  const s = value.trim()
  if (!/^\d{1,5}$/.test(s))
    return false
  const n = Number(s)
  return n >= 1 && n <= 65535
}

export const AddTunnelDialog: Component<AddTunnelDialogProps> = (props) => {
  const tunnelStore = useTunnel()
  const [tunnelType, setTunnelType] = createSignal<'port_forward' | 'socks5'>('port_forward')
  const [targetAddr, setTargetAddr] = createSignal('127.0.0.1')
  const [targetPort, setTargetPort] = createSignal('')
  const [bindAddr, setBindAddr] = createSignal('127.0.0.1')
  const [bindPort, setBindPort] = createSignal('')
  const { submitting, error, run } = useDialogSubmit()

  const targetPortError = createMemo(() => {
    const v = targetPort().trim()
    if (tunnelType() === 'socks5')
      return null
    if (!v)
      return null // will be caught by disabled button
    return isValidPort(v) ? null : 'Must be 1-65535'
  })

  const bindPortError = createMemo(() => {
    const v = bindPort().trim()
    if (!v)
      return null
    return isValidPort(v) ? null : 'Must be 1-65535'
  })

  const targetAddrError = createMemo(() => {
    if (tunnelType() === 'socks5')
      return null
    const v = targetAddr().trim()
    if (!v)
      return 'Required'
    return null
  })

  // The sidecar refuses a non-loopback bind address, because neither tunnel
  // listener authenticates: binding 0.0.0.0 would expose an open gateway into the
  // worker's network to the whole LAN. Mirror that here so the user sees why up
  // front rather than a failed create.
  const bindAddrError = createMemo(() => {
    const v = normalizeBindAddr(bindAddr())
    if (!v)
      return 'Required'
    if (!isLoopbackAddress(v))
      return 'Must be a loopback address (127.0.0.1 or ::1) — the tunnel listener is unauthenticated'
    return null
  })

  const isDisabled = createMemo(() => {
    if (submitting.loading())
      return true
    if (bindAddrError() || bindPortError())
      return true
    if (tunnelType() === 'port_forward') {
      if (!targetPort().trim() || targetPortError() || targetAddrError())
        return true
    }
    return false
  })

  const handleSubmit = (e: Event) => {
    e.preventDefault()
    void run(async () => {
      const effectiveBindPort = bindPort().trim()
        ? Number(bindPort())
        : tunnelType() === 'socks5'
          ? 1080
          : Number(targetPort())
      const tunnel = await tunnelStore!.add({
        workerId: props.workerId,
        type: tunnelType(),
        targetAddr: tunnelType() === 'port_forward' ? targetAddr().trim() : '',
        targetPort: tunnelType() === 'port_forward' ? Number(targetPort()) : 0,
        // Normalized, not raw: the validator strips a `[::1]` bracket pair, so the
        // submitted value must too or the sidecar's net.ParseIP rejects what the
        // dialog just called valid.
        bindAddr: normalizeBindAddr(bindAddr()),
        bindPort: effectiveBindPort,
      })
      props.onCreated(tunnel)
    })
  }

  return (
    <Dialog title="Add tunnel" busy={submitting.loading()} data-testid="add-tunnel-dialog" onClose={() => props.onClose()}>
      <form onSubmit={handleSubmit}>
        <div class={styles.typeSelector}>
          <label class={styles.typeOption}>
            <input
              type="radio"
              name="tunnel-type"
              value="port_forward"
              checked={tunnelType() === 'port_forward'}
              onChange={() => setTunnelType('port_forward')}
            />
            Port forwarding
          </label>
          <label class={styles.typeOption}>
            <input
              type="radio"
              name="tunnel-type"
              value="socks5"
              checked={tunnelType() === 'socks5'}
              onChange={() => setTunnelType('socks5')}
            />
            SOCKS5
          </label>
        </div>

        <Show when={tunnelType() === 'port_forward'}>
          <div class={styles.fieldRow}>
            <div>
              <label>Target address</label>
              <input
                type="text"
                value={targetAddr()}
                onInput={e => setTargetAddr(e.currentTarget.value)}
                placeholder="127.0.0.1"
                data-testid="target-addr"
              />
              <Show when={targetAddrError()}>
                <div class={errorText}>{targetAddrError()}</div>
              </Show>
            </div>
            <div>
              <label>Target port</label>
              <input
                type="text"
                inputMode="numeric"
                value={targetPort()}
                onInput={e => setTargetPort(e.currentTarget.value)}
                placeholder="e.g. 3000"
                data-testid="target-port"
              />
              <Show when={targetPortError()}>
                <div class={errorText}>{targetPortError()}</div>
              </Show>
            </div>
          </div>
        </Show>

        <div class={styles.fieldRow}>
          <div>
            <label>Bind address</label>
            <input
              type="text"
              value={bindAddr()}
              onInput={e => setBindAddr(e.currentTarget.value)}
              placeholder="127.0.0.1"
              data-testid="bind-addr"
            />
            <Show when={bindAddrError()}>
              <div class={errorText}>{bindAddrError()}</div>
            </Show>
          </div>
          <div>
            <label>Bind port</label>
            <input
              type="text"
              inputMode="numeric"
              value={bindPort()}
              onInput={e => setBindPort(e.currentTarget.value)}
              placeholder={tunnelType() === 'socks5' ? '1080' : targetPort() || 'Same as target'}
              data-testid="bind-port"
            />
            <Show when={bindPortError()}>
              <div class={errorText}>{bindPortError()}</div>
            </Show>
          </div>
        </div>

        <Show when={error()}>
          <div class={errorText}>{error()}</div>
        </Show>

        <footer>
          <button type="button" class="outline" disabled={submitting.loading()} onClick={() => props.onClose()} data-testid="tunnel-cancel">
            Cancel
          </button>
          <button type="submit" disabled={isDisabled()} data-testid="tunnel-create">
            <Show when={submitting.loading()}><Spinner /></Show>
            {submitting.loading() ? 'Creating...' : 'Create'}
          </button>
        </footer>
      </form>
    </Dialog>
  )
}
