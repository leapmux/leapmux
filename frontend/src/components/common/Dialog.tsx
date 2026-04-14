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
      aria-label={props.title}
      onClick={(e) => {
        // Close on backdrop click: check if the click landed outside
        // the dialog's bounding rect (i.e. on the ::backdrop).
        if (e.target === dialogRef && !props.busy) {
          const rect = dialogRef.getBoundingClientRect()
          if (e.clientX < rect.left || e.clientX > rect.right || e.clientY < rect.top || e.clientY > rect.bottom) {
            dialogRef.close()
          }
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
