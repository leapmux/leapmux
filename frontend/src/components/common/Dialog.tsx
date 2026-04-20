import type { Component, JSX } from 'solid-js'
import X from 'lucide-solid/icons/x'
import { onCleanup, onMount } from 'solid-js'
import * as styles from './Dialog.css'
import { IconButton, IconButtonState } from './IconButton'

interface DialogProps {
  'title': string
  'tall'?: boolean
  'wide'?: boolean
  'busy'?: boolean
  'class'?: string
  'data-testid'?: string
  'onClose': () => void
  'children': JSX.Element
}

export const Dialog: Component<DialogProps> = (props) => {
  let dialogRef!: HTMLDialogElement
  let unmounting = false
  let pointerDownOnBackdrop = false

  const isOutsideDialogRect = (clientX: number, clientY: number) => {
    const rect = dialogRef.getBoundingClientRect()
    return clientX < rect.left || clientX > rect.right || clientY < rect.top || clientY > rect.bottom
  }

  const handleKeyDown = (e: KeyboardEvent) => {
    if (e.key === 'Escape') {
      e.preventDefault()
      if (!props.busy)
        dialogRef.close()
      return
    }
    if (
      e.key === 'Enter'
      && !e.defaultPrevented
      && !e.isComposing
      && !e.shiftKey && !e.ctrlKey && !e.altKey && !e.metaKey
      && !props.busy
    ) {
      const active = document.activeElement
      if (active instanceof HTMLTextAreaElement || active instanceof HTMLButtonElement || active instanceof HTMLAnchorElement)
        return
      const submitBtn = dialogRef.querySelector('button[type="submit"]') as HTMLButtonElement | null
      if (submitBtn && !submitBtn.disabled) {
        e.preventDefault()
        submitBtn.click()
      }
    }
  }

  onMount(() => {
    dialogRef.showModal()
    const focusTarget = dialogRef.querySelector(
      `.${styles.body} select, .${styles.body} input:not([type="hidden"]), .${styles.body} textarea, .${styles.body} button[type="submit"]`,
    ) as HTMLElement | null
    if (focusTarget)
      focusTarget.focus()
    dialogRef.addEventListener('keydown', handleKeyDown)
  })
  onCleanup(() => {
    unmounting = true
    dialogRef.removeEventListener('keydown', handleKeyDown)
    if (dialogRef.open) {
      dialogRef.close()
    }
  })

  return (
    <dialog
      ref={dialogRef}
      class={`${styles.standard}${props.tall ? ` ${styles.tall}` : ''}${props.wide ? ` ${styles.wide}` : ''}${props.class ? ` ${props.class}` : ''}`}
      data-testid={props['data-testid']}
      data-busy={props.busy ? '' : undefined}
      aria-label={props.title}
      onPointerDown={(e) => {
        // Only treat this as a backdrop press if the pointer went down directly
        // on the dialog element in the ::backdrop area. Presses that start
        // inside the dialog content (e.g. the start of a text selection drag)
        // must not be treated as backdrop interactions, even if the release
        // lands on the backdrop.
        pointerDownOnBackdrop = e.target === dialogRef && isOutsideDialogRect(e.clientX, e.clientY)
      }}
      onClick={(e) => {
        // Close on backdrop click: require both press and release to land on
        // the backdrop, so drags that end on the backdrop don't dismiss.
        const startedOnBackdrop = pointerDownOnBackdrop
        pointerDownOnBackdrop = false
        if (startedOnBackdrop && e.target === dialogRef && !props.busy && isOutsideDialogRect(e.clientX, e.clientY)) {
          dialogRef.close()
        }
      }}
      onClose={() => {
        if (!unmounting && !props.busy)
          props.onClose()
      }}
    >
      <header class={styles.header}>
        <h2>{props.title}</h2>
        <IconButton
          icon={X}
          size="sm"
          class={styles.closeButton}
          state={props.busy ? IconButtonState.Disabled : undefined}
          onClick={() => {
            if (!props.busy)
              props.onClose()
          }}
          aria-label="Close"
        />
      </header>
      <div class={styles.body}>
        {props.children}
      </div>
    </dialog>
  )
}

/** Wraps the top form controls with vertical gap. */
export const DialogTopSection: Component<{ children: JSX.Element }> = props => (
  <div class={styles.topSection}>{props.children}</div>
)

/** Two-column responsive grid for the top row of form controls. */
export const DialogTopRow: Component<{ children: JSX.Element }> = props => (
  <div class={styles.topTwoColumn}>{props.children}</div>
)

/** Switchable one/two-column layout for the main dialog content area. */
export const DialogColumns: Component<{
  twoColumn?: boolean
  left: JSX.Element
  right?: JSX.Element
}> = props => (
  <div class={props.twoColumn !== false ? styles.twoColumn : styles.singleColumn}>
    <div class={styles.leftPanel}>{props.left}</div>
    <div class={props.twoColumn !== false ? styles.rightPanel : undefined}>{props.right}</div>
  </div>
)
