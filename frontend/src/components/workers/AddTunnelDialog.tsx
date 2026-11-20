import type { Component } from 'solid-js'
import type { TunnelInfo, TunnelKeyMismatch } from '~/api/platformBridge'
import { createEffect, createMemo, createSignal, onCleanup, Show } from 'solid-js'
import { actionsFooter } from '~/components/common/actionsFooter.css'
import { Dialog } from '~/components/common/Dialog'
import { Spinner } from '~/components/common/Spinner'
import { useAuth } from '~/context/AuthContext'
import { useTunnel } from '~/context/TunnelContext'
import { useDialogSubmit } from '~/hooks/useDialogSubmit'
import { formatErrorMessage } from '~/lib/errors'
import { isLoopbackAddress, isValidPort, normalizeBindAddr } from '~/lib/ipAddress'
import { onKeyPinsChanged, readWorkerKeyPin } from '~/lib/keyPinStore'
import { errorText } from '~/styles/shared.css'
import * as styles from './AddTunnelDialog.css'
import { verifyWorkerKey } from './verifyWorkerKey'

interface AddTunnelDialogProps {
  workerId: string
  onClose: () => void
  onCreated: (tunnel: TunnelInfo) => void
}

export const AddTunnelDialog: Component<AddTunnelDialogProps> = (props) => {
  const auth = useAuth()
  const tunnelStore = useTunnel()
  const [tunnelType, setTunnelType] = createSignal<'port_forward' | 'socks5'>('port_forward')
  const [targetAddr, setTargetAddr] = createSignal('127.0.0.1')
  const [targetPort, setTargetPort] = createSignal('')
  const [bindAddr, setBindAddr] = createSignal('127.0.0.1')
  const [bindPort, setBindPort] = createSignal('')
  const { submitting, error, setError, run } = useDialogSubmit({
    fallback: 'Could not create the tunnel. Adjust the fields and try again.',
  })
  const [mismatch, setMismatch] = createSignal<TunnelKeyMismatch | null>(null)
  const [verifying, setVerifying] = createSignal(false)
  // Bumped whenever the shared key pin may have moved: this form's own
  // verification, a change another tab committed, or a move of the account
  // namespace. The trust state is a view over durable pin state, so it must
  // follow that state rather than the snapshot it opened with.
  const [pinRevision, setPinRevision] = createSignal(0)
  onCleanup(onKeyPinsChanged(() => setPinRevision(r => r + 1)))

  // The identity scope: the account this form was opened for. A plain
  // snapshot taken at mount (the dialog mounts when the form opens), never a
  // reactive read, so a later identity cannot rewrite what the form admits.
  const identityScope = auth.user()?.id ?? ''
  const identityIsLive = () => (auth.user()?.id ?? '') === identityScope

  // Identity retirement ends the form: a sign-out, or a sign-in as someone
  // else, leaves this submission belonging to an identity that is gone.
  createEffect(() => {
    auth.user() // track
    if (!identityIsLive())
      props.onClose()
  })

  // The Worker key this create admits against: the shared TOFU pin for this
  // Worker. Loaded rather than read in the render, because the pin store
  // answers through a strict transaction; re-loaded after every verification,
  // which is what may have re-pinned the key. A failed read is NOT "no trusted
  // key" — collapsing the two would turn a storage failure into a silent first
  // contact.
  const [pinnedKeyHex, setPinnedKeyHex] = createSignal<string | undefined>(undefined)
  const [pinReadError, setPinReadError] = createSignal<string | null>(null)
  createEffect(() => {
    void pinRevision()
    void (async () => {
      try {
        const { pin } = await readWorkerKeyPin(props.workerId)
        setPinnedKeyHex(pin?.publicKeyHex)
        setPinReadError(null)
      }
      catch (err) {
        setPinnedKeyHex(undefined)
        setPinReadError(formatErrorMessage(err, 'Could not read the trusted Worker key.'))
      }
    })()
  })

  const targetPortError = createMemo(() => {
    const v = targetPort().trim()
    if (tunnelType() === 'socks5')
      return null
    if (!v)
      return null // will be caught by disabled button
    return isValidPort(v)
      ? null
      : 'Must be 1-65535'
  })

  const bindPortError = createMemo(() => {
    const v = bindPort().trim()
    if (!v)
      return null
    return isValidPort(v)
      ? null
      : 'Must be 1-65535'
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

  // A create carries the exact Worker key it admits against, so with no
  // trusted key there is nothing to admit. The verify button establishes it.
  // A read that failed states its own reason instead: it is a storage failure,
  // not a first contact.
  const noTrustedKeyError = createMemo(() => {
    if (pinReadError())
      return pinReadError()
    if (pinnedKeyHex())
      return null
    return 'No trusted Worker key for this Worker yet. Verify the Worker key, then create the tunnel.'
  })

  const isDisabled = createMemo(() => {
    if (submitting.loading() || verifying())
      return true
    if (bindAddrError() || bindPortError() || noTrustedKeyError())
      return true
    if (tunnelType() === 'port_forward') {
      if (!targetPort().trim() || targetPortError() || targetAddrError())
        return true
    }
    return false
  })

  const handleVerify = () => {
    if (verifying())
      return
    setVerifying(true)
    setError(null)
    // Not through `run`: the verify is not a submit, and the submit's loading
    // signal keeps its spinner (and the Create button it disables) up for a
    // debounce window after a fast action settles.
    void (async () => {
      try {
        await verifyWorkerKey(props.workerId)
        // The verify path is what records a key in the shared pin store, so
        // re-read it before the next create admits against it.
        setPinRevision(r => r + 1)
        setMismatch(null)
      }
      catch (err) {
        setError(formatErrorMessage(err, 'Could not verify the Worker key.'))
      }
      finally {
        setVerifying(false)
      }
    })()
  }

  const handleSubmit = (e: Event) => {
    e.preventDefault()
    void run(async () => {
      // Reject a stale submission rather than send it: the form was opened
      // for an identity that is no longer the live one.
      if (!identityIsLive()) {
        props.onClose()
        return
      }
      const effectiveBindPort = bindPort().trim()
        ? Number(bindPort())
        : tunnelType() === 'socks5'
          ? 1080
          : Number(targetPort())
      const expectedWorkerKeysHex = pinnedKeyHex()
      if (!expectedWorkerKeysHex) {
        setError(noTrustedKeyError())
        return
      }
      const result = await tunnelStore!.add({
        workerId: props.workerId,
        type: tunnelType(),
        targetAddr: tunnelType() === 'port_forward'
          ? targetAddr().trim()
          : '',
        targetPort: tunnelType() === 'port_forward'
          ? Number(targetPort())
          : 0,
        // Normalized, not raw: the validator strips a `[::1]` bracket pair, so the
        // submitted value must too or the sidecar's net.ParseIP rejects what the
        // dialog just called valid.
        bindAddr: normalizeBindAddr(bindAddr()),
        bindPort: effectiveBindPort,
        expectedWorkerKeysHex,
        // The identity scope captured at open, not the live one: a reply that
        // arrives after the identity retired must still name who it was for.
        expectedUserId: identityScope,
        channelEpoch: tunnelStore!.channelEpoch(),
      })
      // A reply that lands after the identity retired is stale: it describes a
      // tunnel for an identity this document no longer holds, so nothing of it
      // may land.
      if (!identityIsLive()) {
        props.onClose()
        return
      }
      if ('keyMismatch' in result) {
        setMismatch(result.keyMismatch)
        return
      }
      props.onCreated(result.info)
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
              placeholder={tunnelType() === 'socks5'
                ? '1080'
                : targetPort() || 'Same as target'}
              data-testid="bind-port"
            />
            <Show when={bindPortError()}>
              <div class={errorText}>{bindPortError()}</div>
            </Show>
          </div>
        </div>

        <div class={styles.trustRow}>
          <Show
            when={pinnedKeyHex()}
            fallback={(
              <Show
                when={pinReadError()}
                fallback={<span data-testid="tunnel-no-trusted-key">No trusted Worker key for this Worker yet.</span>}
              >
                <span data-testid="tunnel-key-read-error">{pinReadError()}</span>
              </Show>
            )}
          >
            <span data-testid="tunnel-trusted-key">Worker key trusted on this device.</span>
          </Show>
          <button
            type="button"
            class="outline"
            disabled={verifying()}
            onClick={handleVerify}
            data-testid="tunnel-verify-key"
          >
            <Show when={verifying()}><Spinner /></Show>
            Verify Worker key
          </button>
        </div>

        <Show when={mismatch()}>
          {m => (
            <div class={styles.keyMismatch} role="alert" data-testid="tunnel-key-mismatch">
              <p>
                The Worker keys the Hub announced differ from the key this
                tunnel admits against. One of the two key sets did not come
                from this Worker, so the tunnel was not created.
              </p>
              <p>
                <strong>Expected:</strong>
                {' '}
                <code data-testid="tunnel-expected-fingerprint">{m().expectedFingerprint}</code>
              </p>
              <p>
                <strong>Presented:</strong>
                {' '}
                <code data-testid="tunnel-presented-fingerprint">{m().presentedFingerprint}</code>
              </p>
              <p>
                Confirm the fingerprint against the one the Worker logs when it
                starts, then verify the Worker key before creating the tunnel
                again.
              </p>
            </div>
          )}
        </Show>

        <Show when={error()}>
          <div class={errorText} role="alert" data-testid="tunnel-create-error">{error()}</div>
        </Show>

        <footer class={actionsFooter}>
          <button type="button" class="outline" disabled={submitting.loading()} onClick={() => props.onClose()} data-testid="tunnel-cancel">
            Cancel
          </button>
          <button type="submit" disabled={isDisabled()} data-testid="tunnel-create">
            <Show when={submitting.loading()}><Spinner /></Show>
            {submitting.loading()
              ? 'Creating...'
              : 'Create'}
          </button>
        </footer>
      </form>
    </Dialog>
  )
}
