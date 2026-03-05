import type { Component } from 'solid-js'
import type { KeyPinDecision } from '~/lib/channel'
import { onMount } from 'solid-js'
import { dialogStandard } from '~/styles/shared.css'
import { ConfirmButton } from './ConfirmButton'

interface KeyPinMismatchDialogProps {
  workerId: string
  expectedFingerprint: string
  actualFingerprint: string
  resolve: (decision: KeyPinDecision) => void
}

export const KeyPinMismatchDialog: Component<KeyPinMismatchDialogProps> = (props) => {
  let dlgRef!: HTMLDialogElement

  onMount(() => dlgRef.showModal())

  const handleReject = () => {
    props.resolve('reject')
  }

  const handleAccept = () => {
    props.resolve('accept')
  }

  return (
    <dialog ref={dlgRef} class={dialogStandard} onClose={handleReject} data-testid="key-pin-mismatch-dialog">
      <header><h2>Worker Public Key Changed</h2></header>
      <section>
        <p>
          The public key for worker
          {' '}
          <code>{props.workerId}</code>
          {' '}
          has changed
          since the last connection. This could indicate a legitimate key rotation
          or a potential security issue.
        </p>
        <p>
          <strong>Expected:</strong>
          {' '}
          <code data-testid="expected-fingerprint">{props.expectedFingerprint}</code>
        </p>
        <p>
          <strong>Actual:</strong>
          {' '}
          <code data-testid="actual-fingerprint">{props.actualFingerprint}</code>
        </p>
        <p>
          If you did not expect this change, reject the connection and
          verify the worker's identity before accepting.
        </p>
      </section>
      <footer>
        <button type="button" class="outline" onClick={handleReject} data-testid="key-pin-reject">
          Reject
        </button>
        <ConfirmButton data-variant="danger" onClick={handleAccept} data-testid="key-pin-accept">
          Accept
        </ConfirmButton>
      </footer>
    </dialog>
  )
}
