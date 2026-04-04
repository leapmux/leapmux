import type { Component } from 'solid-js'
import type { TunnelInfo } from '~/api/tunnelApi'
import LoaderCircle from 'lucide-solid/icons/loader-circle'
import { createMemo, createSignal, Show } from 'solid-js'
import { apiLoadingTimeoutMs } from '~/api/transport'
import { Dialog } from '~/components/common/Dialog'
import { Icon } from '~/components/common/Icon'
import { useTunnel } from '~/context/TunnelContext'
import { createLoadingSignal } from '~/hooks/createLoadingSignal'
import { spinner } from '~/styles/animations.css'
import { errorText } from '~/styles/shared.css'
import * as styles from './AddTunnelDialog.css'

interface AddTunnelDialogProps {
  workerId: string
  hubURL: string
  userId: string
  onClose: () => void
  onCreated: (tunnel: TunnelInfo) => void
}

function isValidPort(value: string): boolean {
  const n = Number(value)
  return Number.isInteger(n) && n >= 1 && n <= 65535
}

export const AddTunnelDialog: Component<AddTunnelDialogProps> = (props) => {
  const tunnelStore = useTunnel()
  const [tunnelType, setTunnelType] = createSignal<'port_forward' | 'socks5'>('port_forward')
  const [targetAddr, setTargetAddr] = createSignal('127.0.0.1')
  const [targetPort, setTargetPort] = createSignal('')
  const [bindAddr, setBindAddr] = createSignal('127.0.0.1')
  const [bindPort, setBindPort] = createSignal('')
  const [error, setError] = createSignal<string | null>(null)
  const submitting = createLoadingSignal(apiLoadingTimeoutMs())

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

  const bindAddrError = createMemo(() => {
    const v = bindAddr().trim()
    if (!v)
      return 'Required'
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

  const handleSubmit = async (e: Event) => {
    e.preventDefault()
    setError(null)
    submitting.start()

    const effectiveBindPort = bindPort().trim()
      ? Number(bindPort())
      : tunnelType() === 'socks5'
        ? 1080
        : Number(targetPort())

    try {
      const tunnel = await tunnelStore!.add({
        workerId: props.workerId,
        type: tunnelType(),
        targetAddr: tunnelType() === 'port_forward' ? targetAddr().trim() : '',
        targetPort: tunnelType() === 'port_forward' ? Number(targetPort()) : 0,
        bindAddr: bindAddr().trim(),
        bindPort: effectiveBindPort,
        hubURL: props.hubURL,
        userId: props.userId,
      })
      props.onCreated(tunnel)
    }
    catch (err) {
      setError(err instanceof Error ? err.message : String(err))
      submitting.stop()
    }
  }

  return (
    <Dialog title="Add Tunnel" busy={submitting.loading()} data-testid="add-tunnel-dialog" onClose={() => props.onClose()}>
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
            <Show when={submitting.loading()}><Icon icon={LoaderCircle} size="sm" class={spinner} /></Show>
            {submitting.loading() ? 'Creating...' : 'Create'}
          </button>
        </footer>
      </form>
    </Dialog>
  )
}
