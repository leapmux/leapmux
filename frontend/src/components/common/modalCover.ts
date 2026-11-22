export interface ModalCover {
  /** Mark currently open non-inert dialogs inert and record active focus. */
  cover: () => void
  /** Release dialogs marked by `cover()` and restore the recorded focus. */
  uncover: () => void
}

/**
 * Tracks dialogs covered by an app-root modal host (`ElevationPromptHost`,
 * `SoloStopConfirmHost`).
 *
 * Call `cover()` BEFORE rendering the host's own `<dialog>` so the query
 * captures only dialogs beneath this prompt, and never this host's own dialog.
 * Calling `cover()` again while already active is a no-op so in-place prompt
 * replacement cannot mark the host's own dialog inert.
 *
 * Only app-root hosts mounted as DOM siblings of other dialogs may use this
 * helper. `Dialog.tsx` renders `<dialog>` inline without `<Portal>`, so nested
 * dialogs under `PreferencesDialog` are DOM descendants of their parent
 * `<dialog>`, and setting `inert` on an ancestor `<dialog>` would make the
 * child dialog itself inert.
 */
export function createModalCover(): ModalCover {
  let active = false
  let covered: Element[] = []
  let focusedBefore: HTMLElement | null = null

  const cover = () => {
    if (active)
      return
    active = true
    focusedBefore = document.activeElement instanceof HTMLElement
      ? document.activeElement
      : null
    covered = [
      ...document.querySelectorAll('dialog[open]:not([inert])'),
    ]
    for (const dialog of covered)
      dialog.setAttribute('inert', '')
  }

  // Release `inert` before restoring focus: focus cannot move into an inert
  // subtree. The recorded element can be detached by now, and `focus()` on a
  // detached element is a no-op.
  const uncover = () => {
    if (!active)
      return
    active = false
    for (const dialog of covered)
      dialog.removeAttribute('inert')
    covered = []
    const target = focusedBefore
    focusedBefore = null
    target?.focus()
  }

  return { cover, uncover }
}
