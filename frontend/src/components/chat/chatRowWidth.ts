import type { Accessor } from 'solid-js'
import { batch, createSignal, onCleanup, untrack } from 'solid-js'
import { trailingDebounce } from '~/lib/debounce'

/** The width of the chat rows, and whether a hidden premeasure copy may render at it. */
export interface ChatRowWidth {
  /**
   * Take the width that the pane reports for its rows. A pane with no layout box reports 0.
   *
   * A width above 0 that differs from `held` starts the settle wait again, and `measurable`
   * is false until the wait ends. The first width above 0 starts no wait, so it does not
   * delay the first view. A report of 0 starts none either, and neither does the width that
   * `held` already holds: a pane that gets its layout box back at the same width keeps
   * every height.
   */
  report: (width: number) => void
  /**
   * The one width of the height key and of the hidden premeasure copies: the exact width
   * of the rows, so a copy wraps its text as the row does and measures the height that the
   * row takes. A copy that is a few pixels wider or narrower than the row is wrong by each
   * line that wraps differently: thousands of pixels for a long diff or report on a narrow
   * screen.
   *
   * While the pane reports a width of 0, it keeps the last width above 0, so a pane that
   * loses its layout box and gets it back at the same width keeps every height. Before the
   * first width above 0 it is `undefined`: the rows have no width yet.
   */
  held: Accessor<number | undefined>
  /**
   * Whether a hidden copy may measure at `held`: the pane reports a width above 0 now, and
   * that width held for the settle time. A copy that renders earlier measures a height
   * that the row does not take.
   */
  measurable: Accessor<boolean>
}

/**
 * Create the width state of the chat rows. `settleMs` is how long a new width must hold
 * before `measurable` is true again. A window resize reports a new width in each frame;
 * without the wait, each frame would render a band of hidden copies at a width that the
 * next frame drops.
 *
 * `report` writes the new width and ends the settled state in one update, so no reader
 * sees a new width as measurable before the wait. An effect on the width would be too late
 * for this: Solid runs the render effects of the hidden copies before a user effect, so the
 * copies of the new width would mount once.
 *
 * Call it within a reactive owner: the cleanup of the owner cancels a wait that runs.
 */
export function createChatRowWidth(settleMs: number): ChatRowWidth {
  // The width of the last report. It is 0 before the first report, and for a pane with no
  // layout box.
  const [reported, setReported] = createSignal(0)
  const [held, setHeld] = createSignal<number | undefined>()
  // Whether `held` held for `settleMs`.
  const [settled, setSettled] = createSignal(true)
  const settle = trailingDebounce(() => setSettled(true), settleMs)
  onCleanup(() => settle.cancel())

  const report = (width: number): void => {
    const previous = untrack(held)
    const changed = width > 0 && previous !== undefined && width !== previous
    batch(() => {
      if (changed)
        setSettled(false)
      setReported(width)
      if (width > 0)
        setHeld(width)
    })
    if (changed)
      settle()
  }

  return {
    report,
    held,
    measurable: () => reported() > 0 && settled(),
  }
}
