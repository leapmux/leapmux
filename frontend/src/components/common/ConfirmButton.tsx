import type { Component, JSX } from 'solid-js'
import { createEffect, createSignal, onCleanup, splitProps } from 'solid-js'
import { Tooltip } from './Tooltip'

const RESET_TIMEOUT_MS = 10_000

/**
 * This interface omits `title`, and the omission is the enforcement.
 *
 * Every prop here spreads onto a real `<button>`, so a `title` long enough to
 * state a reason BECOMES the button's accessible name -- and this button's
 * name is STATE ("Confirm?" once armed), which the reason would replace. Wrap
 * the button in a `<Tooltip>` instead; it works on a disabled control and
 * leaves the name alone. `IconButton` omits `title` for the same reason and
 * routes its own `title` prop through `<Tooltip>`.
 */
interface ConfirmButtonProps extends Omit<JSX.ButtonHTMLAttributes<HTMLButtonElement>, 'onClick' | 'title'> {
  /**
   * Content shown after the first click (armed state). Defaults to "Confirm?".
   *
   * Takes an element, not only a string, so an icon-only button can swap its
   * icon for one that shows the next click confirms.
   */
  confirmLabel?: JSX.Element
  /**
   * The tooltip text, and the accessible name, while the button rests.
   *
   * A button with text needs neither this prop nor `confirmTooltip`, because
   * its children already state its name and `confirmLabel` renames it. An
   * icon-only button carries no text: without these two it reaches a screen
   * reader unnamed, and the armed state stays invisible there.
   *
   * This component routes both through `<Tooltip>` itself, exactly as
   * `IconButton` routes its own `title`. A caller cannot do it from outside,
   * because the armed state lives in here.
   */
  tooltip?: string
  /** The tooltip text, and the accessible name, while the button is armed. */
  confirmTooltip?: string
  /** Called only on the second (confirming) click. */
  onClick: () => void
}

/**
 * A two-step confirmation button. The first click arms it (changes label),
 * and only the second click triggers the actual action. Automatically resets
 * on blur or after 10 seconds of inactivity.
 */
export const ConfirmButton: Component<ConfirmButtonProps> = (props) => {
  const [local, buttonProps] = splitProps(props, ['confirmLabel', 'tooltip', 'confirmTooltip', 'onClick', 'children'])
  const [armed, setArmed] = createSignal(false)
  let resetTimer: ReturnType<typeof setTimeout> | undefined
  let blurResetTimer: ReturnType<typeof setTimeout> | undefined

  const clearResetTimer = () => {
    if (resetTimer !== undefined) {
      clearTimeout(resetTimer)
      resetTimer = undefined
    }
  }

  const reset = () => {
    if (blurResetTimer !== undefined) {
      clearTimeout(blurResetTimer)
      blurResetTimer = undefined
    }
    clearResetTimer()
    setArmed(false)
  }

  onCleanup(() => {
    if (blurResetTimer !== undefined) {
      clearTimeout(blurResetTimer)
    }
    clearResetTimer()
  })

  // Disarm when the button becomes disabled. `disabled` is reactive on some
  // callers -- LastTabCloseDialog flips it when a refreshed inspect reports the
  // worktree removal blocked -- and a disabled button that still reads
  // "Confirm?" offers a confirmation for an action that nobody can take. The armed
  // label would otherwise sit there until the 10-second timer or a blur, and
  // neither fires for a control the pointer no longer reaches.
  createEffect(() => {
    if (buttonProps.disabled && armed())
      reset()
  })

  const handleClick = () => {
    if (!armed()) {
      setArmed(true)
      clearResetTimer()
      resetTimer = setTimeout(reset, RESET_TIMEOUT_MS)
    }
    else {
      reset()
      local.onClick()
    }
  }

  // Falls back to the RESTING name rather than to nothing. A caller that names
  // the button once still has a named button while it is armed, and an
  // icon-only control has no other source of a name: without the fallback the
  // armed state reached a screen reader as an unlabelled button.
  const tooltipText = () => (armed() ? (local.confirmTooltip ?? local.tooltip) : local.tooltip)

  const button = (
    <button
      {...buttonProps}
      type="button"
      class={buttonProps.class ?? ''}
      {...(armed() ? { 'data-variant': 'danger' } : {})}
      data-armed={armed() || undefined}
      onClick={handleClick}
      onBlur={() => {
        blurResetTimer = setTimeout(reset, 0)
      }}
    >
      {armed() ? (local.confirmLabel ?? 'Confirm?') : local.children}
    </button>
  )

  // Wrap ONLY for a caller that asked for a tooltip.
  //
  // An unconditional wrapper breaks the callers that supply their OWN
  // `<Tooltip>` around this button -- LastTabCloseDialog and
  // DeleteBranchDialog both do, to state why Delete is disabled. Two tooltips
  // resolve the same `<button>` as their target and both write its
  // `aria-label` and `aria-describedby`, so the inner one erases the reason
  // the outer one published and the button loses its description.
  return (
    <>
      {local.tooltip === undefined && local.confirmTooltip === undefined
        ? button
        : (
            <Tooltip text={tooltipText()} ariaLabel>
              {button}
            </Tooltip>
          )}
    </>
  )
}
