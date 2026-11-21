import type { Component } from 'solid-js'
import type { TunnelConfig, TunnelInfo, TunnelKeyMismatch } from '~/api/platformBridge'
import type { TunnelKeyApproval } from '~/api/workerRpc'
import type { WorkerTrustScope } from '~/context/AuthContext'
import type { Worker } from '~/generated/proto/leapmux/v1/worker_pb'
import { createEffect, createMemo, createSignal, onCleanup, Show, untrack } from 'solid-js'
import { TunnelKeyApprovalExpiredError, TunnelKeyPinMissingError } from '~/api/workerRpc'
import { actionsFooter } from '~/components/common/actionsFooter.css'
import { Dialog } from '~/components/common/Dialog'
import { Spinner } from '~/components/common/Spinner'
import { useAuth } from '~/context/AuthContext'
import { useTunnel } from '~/context/TunnelContext'
import { useDialogSubmit } from '~/hooks/useDialogSubmit'
import { formatErrorMessage } from '~/lib/errors'
import { isLoopbackAddress, isValidPort, normalizeBindAddr } from '~/lib/ipAddress'
import { errorText } from '~/styles/shared.css'
import * as styles from './AddTunnelDialog.css'
import { trustWorkerOnFirstContact, verifyWorkerKey, WorkerKeyPinMissingError, WorkerKeyPinPresentError } from './verifyWorkerKey'

export interface AddTunnelTarget {
  worker: Worker
  scope: WorkerTrustScope
  replacement?: { config: TunnelConfig } & (
    | { approval: TunnelKeyApproval, firstContactRequired?: never }
    | { approval?: never, firstContactRequired: true }
  )
}

interface AddTunnelDialogProps {
  target: AddTunnelTarget
  onClose: () => void
  onCreated: (tunnel: TunnelInfo) => void
}

type FormTrust
  = | { kind: 'ordinary' }
    | { kind: 'needsReview' }
    | { kind: 'needsFirstContact' }
    | { kind: 'approved', approval: TunnelKeyApproval }

export const AddTunnelDialog: Component<AddTunnelDialogProps> = (props) => {
  const auth = useAuth()
  const tunnelStore = useTunnel()
  const target = untrack(() => props.target)
  const scope = target.scope
  const workerId = target.worker.id
  const original = target.replacement?.config
  const [tunnelType, setTunnelType] = createSignal<TunnelConfig['type']>(original?.type ?? 'port_forward')
  const [targetAddr, setTargetAddr] = createSignal(original?.targetAddr ?? '127.0.0.1')
  const [targetPort, setTargetPort] = createSignal(original?.targetPort
    ? String(original.targetPort)
    : '')
  const [bindAddr, setBindAddr] = createSignal(original?.bindAddr ?? '127.0.0.1')
  const [bindPort, setBindPort] = createSignal(original
    ? String(original.bindPort)
    : '')
  const [trust, setTrust] = createSignal<FormTrust>(target.replacement?.firstContactRequired
    ? { kind: 'needsFirstContact' }
    : target.replacement?.approval
      ? { kind: 'approved', approval: target.replacement.approval }
      : { kind: 'ordinary' })
  const [submissionError, setSubmissionError] = createSignal<string | null>(null)
  const [mismatch, setMismatch] = createSignal<TunnelKeyMismatch | null>(null)
  const [verifying, setVerifying] = createSignal(false)
  let disposed = false
  const reviewLifetime = new AbortController()

  const identityIsLive = () => !disposed && auth.workerTrustScope() === scope && !scope.signal.aborted
  const { submitting, formHandler } = useDialogSubmit({
    onError: (err) => {
      if (!identityIsLive())
        return
      if (err instanceof TunnelKeyPinMissingError)
        setTrust({ kind: 'needsFirstContact' })
      else if (err instanceof TunnelKeyApprovalExpiredError)
        setTrust({ kind: 'needsReview' })
      setSubmissionError(formatErrorMessage(err, 'Could not create the tunnel. Adjust the fields and try again.'))
    },
  })

  onCleanup(() => {
    disposed = true
    reviewLifetime.abort()
  })
  createEffect(() => {
    if (!identityIsLive())
      props.onClose()
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

  const isDisabled = createMemo(() => {
    if (submitting.loading() || verifying() || trust().kind === 'needsReview' || trust().kind === 'needsFirstContact')
      return true
    if (bindAddrError() || bindPortError())
      return true
    return tunnelType() === 'port_forward' && (!targetPort().trim() || Boolean(targetPortError() || targetAddrError()))
  })

  const handleVerify = async () => {
    const currentTrust = trust()
    if (!identityIsLive() || verifying()
      || (currentTrust.kind !== 'needsReview' && currentTrust.kind !== 'needsFirstContact')) {
      return
    }
    setVerifying(true)
    setSubmissionError(null)
    try {
      const approval = currentTrust.kind === 'needsFirstContact'
        ? await trustWorkerOnFirstContact(workerId, scope, reviewLifetime.signal)
        : await verifyWorkerKey(workerId, scope, reviewLifetime.signal)
      if (!identityIsLive())
        return
      setTrust({ kind: 'approved', approval })
      setMismatch(null)
    }
    catch (err) {
      if (identityIsLive()) {
        if (err instanceof WorkerKeyPinMissingError)
          setTrust({ kind: 'needsFirstContact' })
        else if (err instanceof WorkerKeyPinPresentError)
          setTrust({ kind: 'needsReview' })
        setSubmissionError(formatErrorMessage(err, 'Could not verify the Worker key.'))
      }
    }
    finally {
      if (identityIsLive())
        setVerifying(false)
    }
  }

  const handleSubmit = formHandler(isDisabled, async () => {
    if (!identityIsLive() || !tunnelStore)
      return
    const currentTrust = trust()
    if (currentTrust.kind === 'needsReview' || currentTrust.kind === 'needsFirstContact')
      return

    setSubmissionError(null)
    const portForward = tunnelType() === 'port_forward'
    const effectiveBindPort = bindPort().trim()
      ? Number(bindPort())
      : portForward
        ? Number(targetPort())
        : 1080
    const result = await tunnelStore.add({
      workerId,
      type: tunnelType(),
      targetAddr: portForward
        ? targetAddr().trim()
        : '',
      targetPort: portForward
        ? Number(targetPort())
        : 0,
      // Submit the same address the validator sees after stripping brackets.
      bindAddr: normalizeBindAddr(bindAddr()),
      bindPort: effectiveBindPort,
    }, scope, currentTrust.kind === 'approved'
      ? currentTrust.approval
      : undefined)
    if (!identityIsLive())
      return
    if (result.kind === 'workerKeyMismatch') {
      setMismatch(result.keyMismatch)
      setTrust({ kind: 'needsReview' })
      return
    }
    props.onCreated(result.info)
  })

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

        <Show when={mismatch()}>
          {m => (
            <div class={styles.keyMismatch} role="alert" data-testid="tunnel-key-mismatch">
              <p>
                The Hub reported a different Worker key.
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
                Compare the presented fingerprint with the one the Worker logs when it starts.
              </p>
            </div>
          )}
        </Show>

        <Show when={submissionError()}>
          <div class={errorText} role="alert" data-testid="tunnel-create-error">{submissionError()}</div>
        </Show>

        <Show when={trust().kind === 'needsReview'}>
          <div class={styles.trustRow}>
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
        </Show>

        <Show when={trust().kind === 'needsFirstContact'}>
          <div class={styles.trustRow}>
            <p>
              The stored Worker key was removed. Establish trust deliberately for the key in the Hub's first announcement. Create remains a separate action after the proof.
            </p>
            <button
              type="button"
              class="outline"
              disabled={verifying()}
              onClick={handleVerify}
              data-testid="tunnel-trust-first-contact"
            >
              <Show when={verifying()}><Spinner /></Show>
              Trust Worker on first contact
            </button>
          </div>
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
