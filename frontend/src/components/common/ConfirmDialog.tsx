import type { Component, JSX } from 'solid-js'
import { Show } from 'solid-js'
import { ConfirmButton } from './ConfirmButton'
import { Dialog } from './Dialog'

interface ConfirmDialogProps {
  title: string
  children: JSX.Element
  confirmLabel?: string
  cancelLabel?: string
  danger?: boolean
  onConfirm: () => void
  onCancel: () => void
}

export const ConfirmDialog: Component<ConfirmDialogProps> = (props) => {
  return (
    <Dialog title={props.title} onClose={() => props.onCancel()}>
      <section>{props.children}</section>
      <footer>
        <button type="button" class="outline" onClick={() => props.onCancel()}>
          {props.cancelLabel ?? 'Cancel'}
        </button>
        <Show
          when={props.danger}
          fallback={(
            <button type="button" onClick={() => props.onConfirm()}>
              {props.confirmLabel ?? 'OK'}
            </button>
          )}
        >
          <ConfirmButton data-variant="danger" onClick={() => props.onConfirm()}>
            {props.confirmLabel ?? 'OK'}
          </ConfirmButton>
        </Show>
      </footer>
    </Dialog>
  )
}
