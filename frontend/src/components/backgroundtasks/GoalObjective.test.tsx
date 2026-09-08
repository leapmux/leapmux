import { fireEvent, render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { installControllableResizeObserver, triggerResizeObserversSync } from '~/test-support/resizeObserverStub'
import { GoalObjective } from './GoalObjective'
import * as styles from './GoalObjective.css'

/**
 * jsdom lays nothing out, so `scrollHeight` and `clientHeight` are both zero and
 * the box can never report that it hides anything. This states the layout the
 * component reads, which is the only input its decision has.
 */
function statesLayout(box: HTMLElement, contentPx: number, boxPx: number) {
  Object.defineProperty(box, 'scrollHeight', { value: contentPx, configurable: true })
  Object.defineProperty(box, 'clientHeight', { value: boxPx, configurable: true })
}

const HOVER_DELAY_MS = 700

describe('goalObjective', () => {
  beforeEach(() => {
    installControllableResizeObserver()
  })

  // The whole point of the change: a goal is markdown, so the card shows what
  // the markup MEANS rather than its asterisks.
  it('renders the objective as markdown', () => {
    const { getByTestId } = render(() => <GoalObjective objective="ship the **auth refactor**" />)
    const box = getByTestId('goal-objective')
    expect(box.querySelector('strong')?.textContent).toBe('auth refactor')
    expect(box.textContent).not.toContain('**')
  })

  it('clamps the objective until the reader opens it', () => {
    const { getByTestId } = render(() => <GoalObjective objective="a goal" />)
    expect(getByTestId('goal-objective').classList.contains(styles.bodyClamped)).toBe(true)
  })

  /**
   * A goal that fits gets no disclosure and no fade. The clamp is still on --
   * it is what caps a later, longer goal -- but nothing is hidden,
   * so a fade there would dim a line the reader can see in full.
   */
  it('offers no disclosure and no fade while the objective fits', () => {
    const { getByTestId, queryByTestId } = render(() => <GoalObjective objective="a short goal" />)
    const box = getByTestId('goal-objective')
    statesLayout(box, 40, 40)
    triggerResizeObserversSync()
    expect(queryByTestId('goal-objective-toggle')).toBeNull()
    expect(box.classList.contains(styles.bodyFaded)).toBe(false)
  })

  it('offers the disclosure and fades the last line once the box hides something', () => {
    const { getByTestId } = render(() => <GoalObjective objective="a long goal" />)
    const box = getByTestId('goal-objective')
    statesLayout(box, 200, 84)
    triggerResizeObserversSync()
    expect(getByTestId('goal-objective-toggle').textContent).toBe('Show more')
    expect(box.classList.contains(styles.bodyFaded)).toBe(true)
  })

  /**
   * A disclosure states whether it is open, and what it opens.
   *
   * Without them a screen reader announces "Show more, button" with no state,
   * and activating it announces nothing at all -- the changed label is the only
   * signal, and a virtual-cursor reader who moved on never receives it.
   */
  it('states its open state and the block it controls', () => {
    const { getByTestId } = render(() => <GoalObjective objective="a long goal" />)
    const box = getByTestId('goal-objective')
    statesLayout(box, 200, 84)
    triggerResizeObserversSync()

    const toggle = getByTestId('goal-objective-toggle')
    expect(toggle.getAttribute('aria-expanded')).toBe('false')
    expect(toggle.getAttribute('aria-controls')).toBe(box.id)
    expect(box.id).not.toBe('')

    fireEvent.click(toggle)

    expect(toggle.getAttribute('aria-expanded')).toBe('true')
  })

  /**
   * The measurement runs only while the box is clamped, and its answer has to
   * survive the expansion. An expanded box hides nothing, so a live measurement
   * would answer "no" and take away the only control that clamps it again.
   */
  it('keeps the disclosure after expanding, so the box can be clamped again', () => {
    const { getByTestId } = render(() => <GoalObjective objective="a long goal" />)
    const box = getByTestId('goal-objective')
    statesLayout(box, 200, 84)
    triggerResizeObserversSync()

    fireEvent.click(getByTestId('goal-objective-toggle'))
    // The expanded box now reports its full height, as a real one would.
    statesLayout(box, 200, 200)
    triggerResizeObserversSync()

    expect(box.classList.contains(styles.bodyClamped)).toBe(false)
    expect(box.classList.contains(styles.bodyFaded)).toBe(false)
    const toggle = getByTestId('goal-objective-toggle')
    expect(toggle.textContent).toBe('Show less')

    fireEvent.click(toggle)
    expect(box.classList.contains(styles.bodyClamped)).toBe(true)
  })

  // An expansion belongs to the text the reader opened, not to the one that
  // replaced it.
  it('collapses again when the objective changes', () => {
    const [objective, setObjective] = createSignal('a long goal')
    const { getByTestId } = render(() => <GoalObjective objective={objective()} />)
    const box = getByTestId('goal-objective')
    statesLayout(box, 200, 84)
    triggerResizeObserversSync()
    fireEvent.click(getByTestId('goal-objective-toggle'))
    expect(box.classList.contains(styles.bodyClamped)).toBe(false)

    setObjective('a different long goal')
    expect(box.classList.contains(styles.bodyClamped)).toBe(true)
  })

  describe('the hover tooltip', () => {
    beforeEach(() => {
      vi.useFakeTimers()
      // A SPY, not an assignment. `~/vitest.setup.ts` installs a working
      // `showPopover` that sets `data-popover-open`, and a bare assignment
      // replaces it with a no-op that `vi.restoreAllMocks()` cannot undo -- so
      // every later case in this file would find `:popover-open` false forever.
      vi.spyOn(HTMLElement.prototype, 'showPopover').mockImplementation(() => {})
    })
    afterEach(() => {
      vi.useRealTimers()
      vi.restoreAllMocks()
    })

    it('shows the whole objective as markdown once the box hides something', () => {
      const { getByTestId } = render(() => <GoalObjective objective="a long **goal**" />)
      const box = getByTestId('goal-objective')
      statesLayout(box, 200, 84)
      triggerResizeObserversSync()

      fireEvent.mouseEnter(box)
      vi.advanceTimersByTime(HOVER_DELAY_MS)

      const tip = screen.getByRole('tooltip', { hidden: true })
      expect(tip.querySelector('strong')?.textContent).toBe('goal')
    })

    /**
     * `Tooltip` resolves `content` eagerly, in a memo, so the objective's
     * markdown must not be built for a card that hides nothing -- and two cards
     * can be on screen at once.
     */
    it('stays silent while the objective fits', () => {
      const { getByTestId } = render(() => <GoalObjective objective="a short goal" />)
      const box = getByTestId('goal-objective')
      statesLayout(box, 40, 40)
      triggerResizeObserversSync()

      fireEvent.mouseEnter(box)
      vi.advanceTimersByTime(HOVER_DELAY_MS)

      expect(screen.queryByRole('tooltip', { hidden: true })).toBeNull()
    })

    // Expanded, the card already shows every line. A tooltip repeating them
    // would cover the text the reader just opened.
    it('stays silent once the reader expands the objective', () => {
      const { getByTestId } = render(() => <GoalObjective objective="a long goal" />)
      const box = getByTestId('goal-objective')
      statesLayout(box, 200, 84)
      triggerResizeObserversSync()
      fireEvent.click(getByTestId('goal-objective-toggle'))

      fireEvent.mouseEnter(box)
      vi.advanceTimersByTime(HOVER_DELAY_MS)

      expect(screen.queryByRole('tooltip', { hidden: true })).toBeNull()
    })
  })
})
