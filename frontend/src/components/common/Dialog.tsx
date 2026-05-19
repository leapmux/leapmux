import type { Component, JSX } from 'solid-js'
import X from 'lucide-solid/icons/x'
import { createSignal, onCleanup, onMount, Show } from 'solid-js'
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

// The open animation (opacity + transform fade-in via
// `@starting-style`) is supplied by @knadh/oat's `dialog.css`. We
// own the close animation here: Solid removes the dialog from the
// DOM the instant the parent's `<Show>` flips, so an exit transition
// keyed off `[open]` being removed never gets to play.
// `beginClose` flips a `closing` marker class that:
//   - reverts the dialog's opacity/transform to Oat's
//     @starting-style values, which Oat's existing transition
//     animates (smooth dialog fade-out), and
//   - triggers our `backdropExit` keyframe so the dim overlay fades
//     out alongside the dialog.
// `onClose` is then deferred by `motion.fast` ms (matching both
// durations) so the parent unmounts only after the exit has played.

export const Dialog: Component<DialogProps> = (props) => {
  let dialogRef!: HTMLDialogElement
  let bodyRef!: HTMLDivElement
  let unmounting = false
  let pointerDownOnBackdrop = false
  let closeTimer: ReturnType<typeof setTimeout> | undefined
  // Drives the `.closing` marker class -- see Dialog.css.ts for the
  // rules it triggers (dialog fade-out + backdrop exit keyframe).
  // Also doubles as a re-entrancy guard so a second Escape / backdrop
  // click while the timer is still running doesn't re-arm beginClose.
  const [closing, setClosing] = createSignal(false)

  const isOutsideDialogRect = (clientX: number, clientY: number) => {
    const rect = dialogRef.getBoundingClientRect()
    return clientX < rect.left || clientX > rect.right || clientY < rect.top || clientY > rect.bottom
  }

  // Set `.closing` so both the dialog's fade-out (Oat's transition
  // animating opacity/transform back to @starting-style values) and
  // the backdrop's exit keyframe run against the still-mounted
  // dialog. Wait `motion.fast` ms before letting the parent unmount
  // us so the exit animations have time to play; the dialog stays in
  // the top layer until `onCleanup` calls `dialogRef.close()`.
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
}> = (props) => {
  return (
    <div class={props.twoColumn !== false ? styles.twoColumn : styles.singleColumn}>
      <div class={styles.leftPanel}>{props.left}</div>
      <Show when={props.twoColumn !== false && props.right}>
        <div class={styles.rightPanel}>{props.right}</div>
      </Show>
    </div>
  )
}
