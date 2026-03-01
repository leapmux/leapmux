import type { Component, JSX } from 'solid-js'
import { createSignal, onCleanup, splitProps } from 'solid-js'

const RESET_TIMEOUT_MS = 10_000

interface ConfirmButtonProps extends Omit<JSX.ButtonHTMLAttributes<HTMLButtonElement>, 'onClick'> {
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

  const clearResetTimer = () => {
    if (resetTimer !== undefined) {
      clearTimeout(resetTimer)
      resetTimer = undefined
    }
  }

  const reset = () => {
    clearResetTimer()
    setArmed(false)
  }

  onCleanup(clearResetTimer)

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
      onBlur={reset}
    >
      {armed() ? (local.confirmLabel ?? 'Confirm?') : local.children}
    </button>
  )
}
