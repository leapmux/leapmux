import type { Component } from 'solid-js'
import type { KeyPinDecision } from '~/lib/keyPinStore'
import { actionsFooter } from '~/components/common/actionsFooter.css'
import { ConfirmButton } from './ConfirmButton'
import { Dialog } from './Dialog'

interface KeyPinMismatchDialogProps {
  workerId: string
  expectedFingerprint: string
  actualFingerprint: string
  resolve: (decision: KeyPinDecision) => void
}

export const KeyPinMismatchDialog: Component<KeyPinMismatchDialogProps> = (props) => {
  const handleReject = () => {
    props.resolve('reject')
  }

  const handleAccept = () => {
    props.resolve('accept')
  }

  return (
    <Dialog title="Worker public key changed" data-testid="key-pin-mismatch-dialog" onClose={handleReject}>
      <section>
        <p>
          The public key for worker
          {' '}
          <code>{props.workerId}</code>
          {' '}
          differs from the key pinned for it at an earlier connection. A worker
          keeps its keys for as long as it stays registered, so one of the two
          keys did not come from this worker.
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
          Reject the connection. Accept only after you confirm that the Actual
          fingerprint matches the one the worker logs when it starts.
        </p>
      </section>
      <footer class={actionsFooter}>
        <button type="button" class="outline" onClick={handleReject} data-testid="key-pin-reject">
          Reject
        </button>
        <ConfirmButton data-variant="danger" onClick={handleAccept} data-testid="key-pin-accept">
          Accept
        </ConfirmButton>
      </footer>
    </Dialog>
  )
}
