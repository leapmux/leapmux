import type { Component, JSX } from 'solid-js'
import X from 'lucide-solid/icons/x'
import { createSignal, onCleanup, onMount } from 'solid-js'
import { motion } from '~/styles/tokens'
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

// The modern way to bind the close animation to `dialogRef.close()`
// would be `@starting-style` + `transition-behavior: allow-discrete`,
// but vanilla-extract's at-rule registry doesn't expose
// `@starting-style`, so we orchestrate manually here. `motion.fast`
// is the same constant the keyframes in `Dialog.css.ts` consume.

export const Dialog: Component<DialogProps> = (props) => {
  let dialogRef!: HTMLDialogElement
  let bodyRef!: HTMLDivElement
  let unmounting = false
  let pointerDownOnBackdrop = false
  let closeTimer: ReturnType<typeof setTimeout> | undefined
  const [closing, setClosing] = createSignal(false)

  const isOutsideDialogRect = (clientX: number, clientY: number) => {
    const rect = dialogRef.getBoundingClientRect()
    return clientX < rect.left || clientX > rect.right || clientY < rect.top || clientY > rect.bottom
  }

  // Begin the close animation; after `motion.fast`, call
  // `props.onClose()` so the parent unmounts us. The dialog stays in
  // the top layer during the animation -- the actual `close()` only
  // happens later, inside `onCleanup`, which is also where programmatic
  // / external close paths land (form submit success, route navigation,
  // etc.) so those paths are instant by design.
  const beginClose = () => {
    if (closing() || props.busy)
      return
    setClosing(true)
    closeTimer = setTimeout(() => {
      closeTimer = undefined
      if (!unmounting)
        props.onClose()
    }, motion.fast)
  }

  const handleKeyDown = (e: KeyboardEvent) => {
    if (e.key === 'Escape') {
      e.preventDefault()
      beginClose()
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
    // Absorb initial focus on the body container (tabindex=-1) so that
    // neither the close button nor any form control gains focus on open.
    // This prevents Enter from closing the dialog and avoids painting a
    // focus ring / auto-scrolling to reveal a form field below the fold.
    bodyRef.focus()
    dialogRef.addEventListener('keydown', handleKeyDown)
  })
  onCleanup(() => {
    unmounting = true
    if (closeTimer)
      clearTimeout(closeTimer)
    dialogRef.removeEventListener('keydown', handleKeyDown)
    if (dialogRef.open) {
      dialogRef.close()
    }
  })

  return (
    <dialog
      ref={dialogRef}
      class={`${styles.standard}${props.tall ? ` ${styles.tall}` : ''}${props.wide ? ` ${styles.wide}` : ''}${props.class ? ` ${props.class}` : ''}`}
      classList={{ [styles.closing]: closing() }}
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
          beginClose()
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
          onClick={() => beginClose()}
          aria-label="Close"
        />
      </header>
      <div ref={bodyRef} class={styles.body} tabindex={-1}>
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
