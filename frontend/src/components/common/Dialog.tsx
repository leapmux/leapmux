import type { Component, JSX } from 'solid-js'
import X from 'lucide-solid/icons/x'
import { onCleanup, onMount } from 'solid-js'
import { dialogCloseButton, dialogHeader, dialogStandard, dialogTall } from '~/styles/shared.css'
import { IconButton } from './IconButton'

interface DialogProps {
  'title': string
  'tall'?: boolean
  'class'?: string
  'data-testid'?: string
  'onClose': () => void
  'children': JSX.Element
}

export const Dialog: Component<DialogProps> = (props) => {
  let dialogRef!: HTMLDialogElement
  let unmounting = false

  onMount(() => dialogRef.showModal())
  onCleanup(() => {
    unmounting = true
    if (dialogRef.open) {
      dialogRef.close()
    }
  })

  return (
    <dialog
      ref={dialogRef}
      class={`${dialogStandard}${props.tall ? ` ${dialogTall}` : ''}${props.class ? ` ${props.class}` : ''}`}
      data-testid={props['data-testid']}
      closedby="any"
      onClose={() => {
        if (!unmounting)
          props.onClose()
      }}
    >
      <header class={dialogHeader}>
        <h2>{props.title}</h2>
        <IconButton
          icon={X}
          size="sm"
          class={dialogCloseButton}
          onClick={() => props.onClose()}
          aria-label="Close"
        />
      </header>
      {props.children}
    </dialog>
  )
}
