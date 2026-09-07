import type { Component, JSX } from 'solid-js'
import { createEffect, createSignal, onCleanup, splitProps } from 'solid-js'
import { Tooltip } from './Tooltip'

const RESET_TIMEOUT_MS = 10_000

/**
 * This interface omits four attributes, and each omission is the enforcement.
 *
 * Every prop here spreads onto a real `<button>`, and the JSX below then sets
 * these four itself. Solid's `mergeProps` gives the later source priority, so a
 * caller's value goes missing in silence.
 *
 * `title`: a title long enough to state a reason BECOMES the button's
 * accessible name -- and this button's name is STATE ("Confirm?" once armed),
 * which the reason would replace. Wrap the button in a `<Tooltip>` instead, or
 * pass `tooltip`. A tooltip works on a disabled control and leaves the name
 * alone. `IconButton` omits `title` for the same reason and routes its own
 * `title` prop through `<Tooltip>`.
 *
 * `onClick`: this component owns the two-click protocol, so it takes the
 * caller's handler under its own name and calls it on the second click alone.
 *
 * `onBlur`: this component owns the blur that disarms it. A caller that must
 * observe blur as well belongs INSIDE the handler below, not layered over it.
 *
 * `type`: always `"button"`, so Enter inside a form cannot submit past the
 * confirmation.
 */
interface ConfirmButtonProps extends Omit<JSX.ButtonHTMLAttributes<HTMLButtonElement>, 'onClick' | 'onBlur' | 'title' | 'type'> {
  /**
   * Content shown after the first click (armed state). Defaults to "Confirm?".
   *
   * Takes an element, not only a string, so an icon-only button can swap its
   * icon for one that shows the next click confirms.
   */
  confirmLabel?: JSX.Element
  /**
   * Why this button is refused, and the id of the element that already states
   * that reason ON SCREEN.
   *
   * ONE object, so the pair cannot appear half-set: a reason with no on-screen
   * element would reach the accessibility tree twice, and an id with no reason
   * describes nothing.
   *
   * A caller passes this instead of wrapping the button in its own `<Tooltip>`.
   * This component always owns its tooltip (see the return below for why an
   * outer one cannot work), so a blocked reason has to come in rather than be
   * layered on. It replaces the tooltip's text while it is set, and points
   * `aria-describedby` at the caller's element, which keeps the button's own
   * name as its name.
   */
  blocked?: { reason: string, reasonId: string }
  /** Called only on the second (confirming) click. */
  onClick: () => void
}

/**
 * The tooltip text for each of the button's two states.
 *
 * `tooltip` is the tooltip text, and the accessible name, while the button
 * rests. `confirmTooltip` is both while the button is armed; without it the
 * resting name stands in both states.
 *
 * A button with TEXT needs neither. Its children already give it a name, and
 * `confirmLabel` changes that name when it arms. An icon-only button carries no
 * text: without these it reaches a screen reader unnamed, and the armed state
 * stays invisible there.
 *
 * The UNION is what stops `confirmTooltip` appearing on its own. That
 * combination leaves an icon-only button unnamed while it rests, and it gains a
 * name only on the first click. A resting fallback to `confirmTooltip` is no
 * answer either, because "Confirm delete?" is the WRONG name for a button that
 * has not armed yet -- so the type refuses the pair rather than picking between
 * two bad values at runtime.
 *
 * This component routes both through `<Tooltip>` itself, exactly as
 * `IconButton` routes its own `title`. A caller cannot do it from outside,
 * because the armed state lives in here.
 */
type ConfirmButtonTooltips = { tooltip: string, confirmTooltip?: string } | { tooltip?: undefined, confirmTooltip?: undefined }

/**
 * A two-step confirmation button. The first click arms it (changes label),
 * and only the second click triggers the actual action. Automatically resets
 * on blur or after 10 seconds of inactivity.
 */
export const ConfirmButton: Component<ConfirmButtonProps & ConfirmButtonTooltips> = (props) => {
  const [local, buttonProps] = splitProps(props, ['confirmLabel', 'tooltip', 'confirmTooltip', 'blocked', 'onClick', 'children'])
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

  // Falls back to the RESTING name rather than to nothing. A caller that gives
  // the button one name still has a named button while it is armed, and an
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

  // This component is the ONLY route to its own tooltip, so it wraps
  // unconditionally rather than when a caller asks.
  //
  // A caller cannot supply the tooltip from outside. One `<Tooltip>` inside
  // another does not merely publish a description twice -- it kills the outer
  // one. `<Tooltip>` renders its child inside a wrapper `<span>`, so an outer
  // tooltip resolves the INNER wrapper as its target: `closest('button')` from
  // that span finds no button, so the outer tooltip stops detecting the
  // disabled state, and once the inner tooltip adds its own offscreen
  // description the outer wrapper holds two element children and
  // `resolveTargetEl` gives up entirely. A caller with a blocked reason passes
  // `blocked`, which routes through this one tooltip.
  //
  // `ariaLabel` carries the button's own NAME, and never the blocked reason. A
  // reason long enough to be useful BECOMES the accessible name if it goes
  // there, which is the exact failure `title` is banned for. So the reason
  // travels as a DESCRIPTION, pointed at the element the caller already renders
  // on screen. A button with text passes no `tooltip`, so `ariaLabel` is
  // undefined and its own children keep naming it.
  //
  // With neither `tooltip` nor `blocked`, `text` is undefined: `<Tooltip>` then
  // has nothing to show, keeps its wrapper at `display: contents`, and installs
  // no observer. `IconButton` wraps unconditionally on the same terms.
  return (
    <Tooltip
      text={local.blocked?.reason ?? tooltipText()}
      describedBy={local.blocked?.reasonId}
      ariaLabel={tooltipText()}
    >
      {button}
    </Tooltip>
  )
}
