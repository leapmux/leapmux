import type { JSXElement } from 'solid-js'
import { createEffect, createMemo, createSignal, For, onCleanup, onMount, Show } from 'solid-js'
import { createRafResizeObserver } from '~/lib/resizeObserver'
import { nextFilterTab } from './FilterTabBar'
import * as styles from './PillGroup.css'
import { Tooltip } from './Tooltip'

/** One choice in a {@link PillGroup}. */
export interface PillOptionSpec<T> {
  value: T
  label: JSXElement
  /**
   * Refuse this option and explain the reason.
   *
   * Keep an option visible when the reader can restore it. The option shows
   * what exists and what action restores it. Remove options that the
   * deployment does not support.
   *
   * The tooltip works on a disabled button. Its reason does not replace the
   * accessible name of the button.
   */
  disabledReason?: string
}

interface IndicatorMetrics {
  left: number
  width: number
}

/**
 * One radio in the group.
 *
 * Selection once used only a CSS class at twelve call sites. A screen reader
 * then described each option as the same stateless button.
 *
 * A radio gives the correct one-of-N semantics. A toggle button would promise
 * that the reader can clear the selected option. This control does not permit
 * that action. A toggle button also lacks the group and set position.
 *
 * A radiogroup can announce "Theme, Dark, radio button, checked, 2 of 3."
 *
 * Keep this component private. A radio outside a radiogroup does not give the
 * reader the required group context. Use `aria-pressed` for a real two-state
 * button that the reader can clear.
 */
function PillOption(props: {
  selected: boolean
  /** Whether this radio holds the single tab stop for the group. */
  roving: boolean
  /** Whether this radio refuses an input. */
  disabled: boolean
  /** Whether this option dims independently from the group. */
  dimmed: boolean
  /** Whether a divider precedes this option. */
  separated: boolean
  /** Why this option refuses an input. */
  disabledReason?: string
  onClick: () => void
  ref?: (el: HTMLButtonElement) => void
  children: JSXElement
}) {
  const pill = () => (
    <button
      // A button in a form submits by default. This button only selects an
      // option, so it must never submit the form.
      //
      // The old default made the Passkey option submit the login form. The
      // incomplete request left the submit button in its "Signing in" state.
      type="button"
      class={styles.pillOption}
      classList={{
        [styles.pillOptionActive]: props.selected,
        [styles.pillOptionDimmed]: props.dimmed,
        [styles.pillOptionSeparated]: props.separated,
      }}
      role="radio"
      aria-checked={props.selected}
      // The native attribute supplies `aria-disabled` through the browser. It
      // also removes the radio from the tab order.
      disabled={props.disabled}
      // The roving option owns the tab stop. Do not derive this value directly
      // from `selected`. A stale stored value can match no current option.
      //
      // The radio pattern permits an unchecked radio to own the tab stop. This
      // keeps the group reachable when no option is checked.
      tabIndex={props.roving && !props.disabled ? 0 : -1}
      ref={el => props.ref?.(el)}
      onClick={() => props.onClick()}
    >
      {props.children}
    </button>
  )

  // A tooltip adds listeners and an observer. Mount it only when it has a
  // reason to show.
  return (
    <Show when={props.disabledReason} fallback={pill()}>
      {reason => <Tooltip text={reason()}>{pill()}</Tooltip>}
    </Show>
  )
}

/**
 * A one-of-N segmented control with radiogroup semantics.
 *
 * The label gives the group its accessible name. A visible heading above the
 * group does not create this association in the accessibility tree.
 *
 * Arrow, Home, and End keys use the shared roving-tabindex calculation from
 * FilterTabBar. This keeps one keyboard implementation for both controls.
 */
export function PillGroup<T>(props: {
  label: string
  options: PillOptionSpec<T>[]
  selected: (value: T) => boolean
  onSelect: (value: T) => void
  /**
   * Show the current selection and refuse changes.
   *
   * The theme chooser uses this state while "Match UI theme" controls its mode.
   * The visible selection shows the value that the governing control produced.
   * It also shows what disabling that control will restore.
   */
  disabled?: boolean
}) {
  // `<For>` keeps a row when its position changes. It does not call the ref
  // again after that move. Store elements by value so focus and indicator
  // measurements continue to use the correct button.
  const els = new Map<T, HTMLButtonElement>()
  const noSelection = Symbol('no selection')
  const [indicatorMetrics, setIndicatorMetrics] = createSignal<IndicatorMetrics>()
  const [indicatorMoves, setIndicatorMoves] = createSignal(false)
  let groupEl: HTMLDivElement | undefined
  let resizeObserver: ReturnType<typeof createRafResizeObserver>
  let measureScheduled = false
  let measureFrame: number | undefined
  let pendingMotion = false
  let hasIndicatorMetrics = false

  /** Whether the group or the option refuses an input. */
  const refused = (opt: PillOptionSpec<T>) =>
    props.disabled === true || opt.disabledReason !== undefined

  /** The option that supplies the active fill. */
  const selectedOption = () => props.options.find(opt => props.selected(opt.value))

  /** The values that a keyboard input can reach. */
  const reachable = createMemo(() => props.options.filter(opt => !refused(opt)).map(opt => opt.value))

  /**
   * The option that owns the single tab stop.
   *
   * Use the selected option when it accepts input. Otherwise, use the first
   * reachable option. A stored browser preference can match no current option
   * after a schema change.
   *
   * A refused selection passes the tab stop to a reachable option. The radio
   * pattern permits an unchecked radio to own that tab stop.
   *
   * A group with no reachable option keeps the calculated owner, but the
   * disabled button receives `tabIndex=-1`. The group still reports a selected
   * value when one exists.
   */
  const rovingIndex = createMemo(() => {
    const selected = props.options.findIndex(opt => props.selected(opt.value))
    if (selected >= 0 && !refused(props.options[selected]!))
      return selected
    const firstReachable = props.options.findIndex(opt => !refused(opt))
    if (firstReachable >= 0)
      return firstReachable
    return selected < 0 ? 0 : selected
  })

  const measureIndicator = (move: boolean) => {
    const option = selectedOption()
    const selectedEl = option === undefined ? undefined : els.get(option.value)
    if (!groupEl || !selectedEl?.isConnected) {
      hasIndicatorMetrics = false
      setIndicatorMoves(false)
      setIndicatorMetrics(undefined)
      return
    }

    // DOM offsets round to whole CSS pixels, but text can give a segment a
    // fractional width. Normalize viewport rectangles against an ancestor
    // scale so the fill keeps the exact layout size during dialog motion.
    const groupRect = groupEl.getBoundingClientRect()
    const selectedRect = selectedEl.getBoundingClientRect()
    const layoutWidth = Number.parseFloat(getComputedStyle(groupEl).width)
    const hasViewportGeometry = groupRect.width > 0
      && selectedRect.width > 0
      && Number.isFinite(layoutWidth)
      && layoutWidth > 0
    const scaleX = hasViewportGeometry ? groupRect.width / layoutWidth : 1
    const left = hasViewportGeometry
      ? (selectedRect.left - groupRect.left) / scaleX - groupEl.clientLeft + groupEl.scrollLeft
      : selectedEl.offsetLeft
    const width = hasViewportGeometry ? selectedRect.width / scaleX : selectedEl.offsetWidth

    // An existing indicator can move between selections. The first valid
    // measurement must appear at its destination without entrance motion.
    setIndicatorMoves(move && hasIndicatorMetrics)
    hasIndicatorMetrics = true
    setIndicatorMetrics({ left, width })
  }

  /** Measure after Solid commits option changes to the DOM. */
  const scheduleIndicatorMeasure = (move: boolean) => {
    pendingMotion ||= move
    if (measureScheduled)
      return

    measureScheduled = true
    let firedSynchronously = false
    const frame = requestAnimationFrame(() => {
      firedSynchronously = true
      measureScheduled = false
      measureFrame = undefined
      const shouldMove = pendingMotion
      pendingMotion = false
      measureIndicator(shouldMove)
    })

    // The test environment runs requestAnimationFrame synchronously. Do not
    // retain a completed frame in that environment.
    if (measureScheduled && !firedSynchronously)
      measureFrame = frame
  }

  let previousSelection: T | typeof noSelection = noSelection
  createEffect(() => {
    const option = selectedOption()
    const selection = option === undefined ? noSelection : option.value
    const changedSelection = previousSelection !== noSelection
      && selection !== noSelection
      && !Object.is(previousSelection, selection)
    previousSelection = selection
    scheduleIndicatorMeasure(changedSelection)
  })

  onMount(() => {
    // Observe every segment because a label or loaded font can change one
    // segment without changing the selected value.
    resizeObserver = createRafResizeObserver(() => measureIndicator(false))
    if (groupEl)
      resizeObserver?.observe(groupEl)
    for (const el of els.values())
      resizeObserver?.observe(el)
    scheduleIndicatorMeasure(false)
  })

  onCleanup(() => {
    resizeObserver?.disconnect()
    if (measureFrame !== undefined)
      cancelAnimationFrame(measureFrame)
    measureScheduled = false
    pendingMotion = false
  })

  const selectAt = (value: T | undefined) => {
    const opt = props.options.find(option => option.value === value)
    if (!opt || refused(opt))
      return
    props.onSelect(opt.value)
    els.get(opt.value)?.focus()
  }

  const onKeyDown = (event: KeyboardEvent) => {
    const values = reachable()
    if (values.length === 0)
      return

    // Focus sits on the option that owns the tab stop. Start the keyboard
    // calculation there when no selected value exists. A missing origin would
    // make each arrow start from the first option.
    const origin = props.options[rovingIndex()]?.value
    const next = nextFilterTab(values, values.includes(origin!) ? origin : values[0], event.key)
    if (next === undefined)
      return
    event.preventDefault()
    selectAt(next)
  }

  // An empty radiogroup exposes no choice and draws a two-pixel border box.
  // Render nothing until at least one option exists.
  return (
    <Show when={props.options.length > 0}>
      <div
        ref={(el) => {
          groupEl = el
          resizeObserver?.observe(el)
          onCleanup(() => {
            resizeObserver?.unobserve(el)
            if (groupEl === el)
              groupEl = undefined
          })
        }}
        class={styles.pillGroup}
        classList={{ [styles.pillGroupDisabled]: props.disabled === true }}
        role="radiogroup"
        aria-label={props.label}
        onKeyDown={onKeyDown}
      >
        <Show when={indicatorMetrics()}>
          {metrics => (
            <span
              class={styles.selectionIndicator}
              classList={{
                [styles.selectionIndicatorDimmed]: props.disabled !== true
                  && selectedOption()?.disabledReason !== undefined,
                [styles.selectionIndicatorMoves]: indicatorMoves(),
              }}
              aria-hidden="true"
              style={{
                transform: `translateX(${metrics().left}px)`,
                width: `${metrics().width}px`,
              }}
            />
          )}
        </Show>
        <For each={props.options}>
          {(opt, index) => (
            <PillOption
              selected={props.selected(opt.value)}
              roving={index() === rovingIndex()}
              disabled={refused(opt)}
              dimmed={props.disabled !== true && opt.disabledReason !== undefined}
              separated={index() > 0}
              disabledReason={opt.disabledReason}
              onClick={() => selectAt(opt.value)}
              ref={(el) => {
                els.set(opt.value, el)
                resizeObserver?.observe(el)
                // Solid does not call a ref with null during disposal. A shorter
                // option list could otherwise retain a detached button.
                //
                // Remove only the matching entry. A remove-and-add operation can
                // give the same value to a new element before cleanup runs.
                onCleanup(() => {
                  resizeObserver?.unobserve(el)
                  if (els.get(opt.value) === el)
                    els.delete(opt.value)
                })
              }}
            >
              {opt.label}
            </PillOption>
          )}
        </For>
      </div>
    </Show>
  )
}
