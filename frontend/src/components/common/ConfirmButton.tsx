import type { Component, JSX } from 'solid-js'
import { createEffect, createSignal, onCleanup, splitProps } from 'solid-js'

const RESET_TIMEOUT_MS = 10_000

/**
 * `title` is omitted, and the omission is the enforcement.
 *
 * Every prop here spreads onto a real `<button>`, so a `title` long enough to
 * state a reason BECOMES the button's accessible name -- and this button's
 * name is STATE ("Confirm?" once armed), which the reason would replace. Wrap
 * the button in a `<Tooltip>` instead; it works on a disabled control and
 * leaves the name alone. `IconButton` omits `title` for the same reason and
 * routes its own `title` prop through `<Tooltip>`.
 */
interface ConfirmButtonProps extends Omit<JSX.ButtonHTMLAttributes<HTMLButtonElement>, 'onClick' | 'title'> {
  /** Label shown after the first click (armed state). Defaults to "Confirm?". */
  confirmLabel?: string
  /** Called only on the second (confirming) click. */
  onClick: () => void
}

/**
 * A two-step confirmation button. The first click arms it (changes label),
 * and only the second click triggers the actual action. Automatically resets
 * on blur or after 10 seconds of inactivity.
 */
export const ConfirmButton: Component<ConfirmButtonProps> = (props) => {
  const [local, buttonProps] = splitProps(props, ['confirmLabel', 'onClick', 'children'])
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
  // "Confirm?" offers a confirmation for an action nobody can take. The armed
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

  return (
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
}
