import type { Component, JSX } from 'solid-js'
import { onMount, Show } from 'solid-js'
import { dialogStandard } from '~/styles/shared.css'
import { ConfirmButton } from './ConfirmButton'

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
  let dlgRef!: HTMLDialogElement

  onMount(() => dlgRef.showModal())

  return (
    <dialog ref={dlgRef} class={dialogStandard} closedby="any" onClose={() => props.onCancel()}>
      <header><h2>{props.title}</h2></header>
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
          <ConfirmButton class="danger" onClick={() => props.onConfirm()}>
            {props.confirmLabel ?? 'OK'}
          </ConfirmButton>
        </Show>
      </footer>
    </dialog>
  )
}
