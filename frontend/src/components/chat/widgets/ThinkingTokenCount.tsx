import type { Component } from 'solid-js'
import { createEffect, createMemo, For, Index, onCleanup, onMount, Show } from 'solid-js'
import { createStore } from 'solid-js/store'
import { prefersReducedMotion } from '~/lib/prefersReducedMotion'
import { formatTokenCount } from '../rendererUtils'
import * as styles from './ThinkingTokenCount.css'

// The strip repeats 0-9 several times so a forward roll always has cells ahead
// of it; `pos` (the shown cell index) only increases between resets, and a
// settled roll folds it back by whole 0-9 cycles — invisibly, since a cell and
// its lower copy render the same digit. RESET_HEADROOM cells are kept below the
// top of the strip as slack for update bursts that outpace the roll.
//
// Load-bearing invariant tying these together: after the eager reset (which
// fires when pos + delta >= STRIP_CELLS - RESET_HEADROOM), pos is folded into
// [0, 9] and then advanced by delta <= 9, so the post-update pos is at most 18;
// without a reset it is at most (STRIP_CELLS - RESET_HEADROOM - 1) + 9. Both
// must stay < STRIP_CELLS so the target cell exists. That holds as long as
// RESET_HEADROOM >= max-delta (9) AND STRIP_CELLS - RESET_HEADROOM > max-delta,
// i.e. RESET_HEADROOM in [9, STRIP_CELLS - 10]. The 30/10 split satisfies it
// with margin; a runtime assert below fails fast if someone breaks it.
const STRIP_CELLS = 30
const RESET_HEADROOM = 10
const MAX_FORWARD_DELTA = 9
if (RESET_HEADROOM < MAX_FORWARD_DELTA || STRIP_CELLS - RESET_HEADROOM <= MAX_FORWARD_DELTA) {
  throw new Error(
    `odometer strip misconfigured: RESET_HEADROOM=${RESET_HEADROOM}, STRIP_CELLS=${STRIP_CELLS} `
    + `must satisfy ${MAX_FORWARD_DELTA} <= RESET_HEADROOM <= ${STRIP_CELLS - MAX_FORWARD_DELTA}`,
  )
}
const STRIP_DIGITS = Array.from({ length: STRIP_CELLS }, (_, i) => i % 10)
const ROLL_TRANSITION = 'transform 0.32s cubic-bezier(0.2, 0.75, 0.25, 1)'

// The imperative digit roll reads prefersReducedMotion() (from ~/lib) fresh on
// every roll rather than caching it, so toggling the OS setting mid-turn stops
// (or resumes) the roll immediately; the CSS @media rules react to the same
// change for the fade animations, keeping the JS-driven roll in step.

// Forward-only advance from one digit to the next: 9->0 is +1 (up through the
// wrap), 2->5 is +3, an unchanged digit is 0. Never negative, so accumulating
// it into `pos` keeps the strip rolling strictly upward.
//
// INTENTIONAL: the roll is forward-only by design — an odometer counts up. The
// thinking-token estimate is a cumulative per-turn count, monotonic within the
// continuous display the counter shows (it is cleared at every turn/phase
// boundary), so a digit never needs to roll backward. If a value ever did
// decrease mid-display, the columns would spin forward to the smaller number
// rather than reverse; that is the accepted trade for the always-up aesthetic,
// not a bug to "fix".
export function forwardDelta(prevDigit: number, nextDigit: number): number {
  return (nextDigit - prevDigit + 10) % 10
}

// The unit family of a formatted count: '' (bare integer), 'k', or 'M'. Values
// in the same family share a digit structure (right-aligned: ones, ., tenths,
// unit), so a count growing a leading digit (9.9k -> 10.0k, 99 -> 100) keeps the
// existing columns — they roll, the unit/point stay, and the new leading column
// fades in. Crossing 1k/1M changes family: the number re-scales (999 -> 1.0k has
// no digit-to-digit mapping), so those transitions crossfade instead.
export function shapeFamily(formatted: string): string {
  const last = formatted[formatted.length - 1]
  return last === 'k' || last === 'M' ? last : ''
}

// A single digit position. An in-flow hidden sizer gives the column the real
// tabular-digit width/height (so it matches the ghost); the strip rides
// absolutely on top and is rolled imperatively so it always travels upward —
// 9->0 continues up through the wrap (a +1 advance) instead of snapping back.
const DigitColumn: Component<{ digit: number, enter: boolean }> = (props) => {
  let stripEl!: HTMLSpanElement
  // pos = the strip cell index currently shown. Monotonic between resets, so
  // the strip only ever rolls up. pos % 10 is the visible digit.
  // eslint-disable-next-line solid/reactivity -- intentional one-time initial read; the effect below tracks changes.
  let pos = props.digit
  // Count of roll transitions started but not yet ended/cancelled. The settle
  // fold (below) runs only when this returns to 0 — i.e. the LATEST roll has
  // come to rest — so a stale transitionend from a roll the burst-reset already
  // interrupted can never fold (and thus snap) the strip mid-animation.
  let pending = 0

  // Current rendered translateY of the strip, in cell units (negative = rolled
  // up), read from the live computed matrix so it reflects an in-flight roll's
  // interpolated position — not the inline target. null when the browser can't
  // report a matrix (jsdom, or no layout), so callers fall back to the settled
  // target.
  const liveOffsetCells = (): number | null => {
    const m = /matrix\([^)]*,\s*([-\d.e+]+)\)/.exec(getComputedStyle(stripEl).transform)
    if (!m)
      return null
    const cellPx = stripEl.getBoundingClientRect().height / STRIP_CELLS
    if (!cellPx)
      return null
    return Number.parseFloat(m[1]) / cellPx
  }

  // Commit an offset (in cells, negative = up) to the strip WITHOUT animating:
  // disable the transition, set the transform, force a reflow so the jump
  // commits, then re-arm the roll transition for the next change.
  const placeInstant = (offsetCells: number) => {
    stripEl.style.transition = 'none'
    stripEl.style.transform = `translateY(calc(${offsetCells} * var(--cell)))`
    stripEl.getBoundingClientRect()
    stripEl.style.transition = prefersReducedMotion() ? 'none' : ROLL_TRANSITION
  }

  // Apply pos to the strip. animate=false commits instantly (initial placement,
  // cycle folds); animate=true rolls there.
  const apply = (animate: boolean) => {
    if (!animate) {
      placeInstant(-pos)
      return
    }
    pending++
    // Re-arm the roll transition before the transform write: a prior reduced-
    // motion placeInstant leaves it 'none', and prefersReducedMotion() flipping
    // back is a plain read with no DOM write, so without this an animated roll
    // after the OS setting is re-enabled mid-turn would snap instead of roll AND
    // leak a `pending` with no transitionend to balance it. No-op once already
    // ROLL_TRANSITION; assigning it doesn't itself start a transition (only the
    // transform change below, against a live transition, does), and no reflow is
    // forced between the two writes so they land in one style-change event.
    stripEl.style.transition = ROLL_TRANSITION
    stripEl.style.transform = `translateY(calc(${-pos} * var(--cell)))`
  }

  // Fold pos down by whole 0-9 cycles. The landing cell and the copy 10 below it
  // render the same digit, so a whole-cycle fold is invisible. Mid-animation the
  // strip sits at a fractional offset, so snapping to the folded *settled* cell
  // (-pos) would jump by that in-flight fraction; instead shift the live
  // fractional offset down by the same whole cycles. At rest, or when the offset
  // can't be read (jsdom), -pos IS the exact position, so use it directly.
  const foldCycle = () => {
    const cycles = Math.floor(pos / 10)
    if (cycles <= 0)
      return
    pos -= cycles * 10
    const live = liveOffsetCells()
    placeInstant(live === null ? -pos : live + cycles * 10)
  }

  // A roll transition ended (`settled`) or was interrupted by a newer one
  // (`!settled`, a transitioncancel); a started roll fires exactly one. Decrement
  // the in-flight counter, then on a genuine end fold the cycle only if the strip
  // has actually come to rest — its live offset matches the target -pos, meaning
  // no newer roll superseded this one. That geometric at-rest check also
  // reconciles `pending` to 0, so a browser that elides a transitioncancel can't
  // leave it stuck > 0 and permanently disable the fold. When the offset can't be
  // read (jsdom), fall back to the pending===0 counter.
  const onRollDone = (settled: boolean) => (e: TransitionEvent) => {
    if (e.propertyName !== 'transform')
      return
    if (pending > 0)
      pending--
    if (!settled)
      return
    const live = liveOffsetCells()
    const atRest = live === null ? pending === 0 : Math.abs(live + pos) < 0.01
    if (!atRest)
      return
    pending = 0
    if (pos >= 10)
      foldCycle()
  }
  const onEnd = onRollDone(true)
  const onCancel = onRollDone(false)

  onMount(() => {
    apply(false)
    stripEl.addEventListener('transitionend', onEnd)
    stripEl.addEventListener('transitioncancel', onCancel)
  })
  onCleanup(() => {
    stripEl.removeEventListener('transitionend', onEnd)
    stripEl.removeEventListener('transitioncancel', onCancel)
  })

  createEffect(() => {
    const next = props.digit
    if (prefersReducedMotion()) {
      // No animation to preserve direction — just show the digit.
      pos = next
      apply(false)
      return
    }
    const prevDigit = pos % 10
    if (next === prevDigit)
      return
    // Forward advance only: 9->0 is +1 (rolls up through the wrap), never -9.
    const delta = forwardDelta(prevDigit, next)
    // Safety for bursts that outpace transitionend: fold down before the strip
    // runs out. Only bites on the fast-spinning low digit, where it's unseen.
    if (pos + delta >= STRIP_CELLS - RESET_HEADROOM)
      foldCycle()
    pos += delta
    apply(true)
  })

  return (
    <span
      classList={{ [styles.column]: true, [styles.slotEnter]: props.enter }}
      data-testid="odo-digit"
      // The TARGET digit, not the one currently painted: the visible glyph is
      // driven by the imperative strip transform and lags during a roll. Tests
      // read this to assert the logical value without waiting on the animation;
      // don't "fix" it to track `pos` or the odometer test helpers break.
      data-digit={props.digit}
    >
      <span class={styles.columnSizer} aria-hidden="true">0</span>
      <span ref={stripEl} class={styles.strip}>
        <Index each={STRIP_DIGITS}>{d => <span class={styles.stripCell}>{d()}</span>}</Index>
      </span>
    </span>
  )
}

// Renders one formatted value as a row of rolling digit columns and static
// chars. The chars are reversed and laid out right-to-left (the layer is
// flex-direction: row-reverse), so Index keys each slot by its distance from
// the RIGHT edge: a count growing a leading digit appends a slot (the existing
// columns keep their identity and roll) instead of shifting every position.
// `animateEntry` fades freshly-mounted slots in — the new leading digit on
// growth, and every slot of a freshly-swapped live layer on a unit crossfade;
// the outgoing snapshot passes false so it only fades out as a whole.
const RollingNumber: Component<{ value: string, animateEntry: boolean }> = (props) => {
  const chars = createMemo(() => [...props.value].reverse())
  const isDigit = (ch: string) => ch >= '0' && ch <= '9'
  return (
    <Index each={chars()}>
      {ch => (
        <Show
          when={isDigit(ch())}
          fallback={(
            <span classList={{ [styles.staticChar]: true, [styles.slotEnter]: props.animateEntry }}>
              {ch()}
            </span>
          )}
        >
          <DigitColumn digit={Number(ch())} enter={props.animateEntry} />
        </Show>
      )}
    </Index>
  )
}

// Keeps prior values of `value` around as fading-out snapshots, minting one
// whenever `shouldSnapshot(prev, next)` fires and removing it after `durationMs`
// (matched to the CSS fade). The crossfade bookkeeping — id minting, the removal
// timers, and their cleanup — lives here so the consumer reads as declarative
// layout and renders the returned list as fading-out layers. A `hasPrev` flag
// (not an `undefined` sentinel for `prev`) tracks first appearance, so the hook
// stays honest for a `T` that legitimately includes `undefined`.
function useFadingSnapshots<T>(
  value: () => T,
  options: { shouldSnapshot: (prev: T, next: T) => boolean, durationMs: number },
): { id: number, value: T }[] {
  const [snapshots, setSnapshots] = createStore<{ id: number, value: T }[]>([])
  let nextId = 1
  const timers = new Set<ReturnType<typeof setTimeout>>()
  let hasPrev = false
  let prev!: T
  createEffect(() => {
    const next = value()
    if (hasPrev && options.shouldSnapshot(prev, next)) {
      const id = nextId++
      const snapshot = prev
      // Append via the functional updater rather than `setSnapshots(length, …)`:
      // reading `snapshots.length` inside this tracked effect would subscribe it
      // to the store, so a later removal (which shrinks the list) would re-run
      // the effect for no reason. The functional form depends only on `value()`.
      setSnapshots(rows => [...rows, { id, value: snapshot }])
      const timer = setTimeout(() => {
        timers.delete(timer)
        setSnapshots(rows => rows.filter(row => row.id !== id))
      }, options.durationMs)
      timers.add(timer)
    }
    prev = next
    hasPrev = true
  })
  onCleanup(() => {
    for (const timer of timers)
      clearTimeout(timer)
  })
  return snapshots
}

/**
 * Animated thinking-token count: "<n> tokens" where the number's digits roll up
 * while the digit-shape is stable and crossfade when the format changes.
 *
 * The number's width and baseline come from an in-flow hidden ghost; the visible
 * rolling digits are painted by absolutely-positioned overlays on top of it, so
 * the overlays' clipped (baseline-less) columns never disturb where the count
 * sits next to " tokens" and the verb.
 */
export const ThinkingTokenCount: Component<{ tokens: number }> = (props) => {
  const display = createMemo(() => formatTokenCount(props.tokens, 2))
  // Single-item list keyed by the unit family: For reuses the live layer while
  // the family is stable (so digits roll and a new leading column fades in) and
  // remounts it — for the unit crossfade — only when the family changes.
  const familyKey = createMemo(() => [shapeFamily(display())])

  // On a unit-family change (crossing 1k/1M) the number re-scales with no
  // digit-to-digit mapping, so snapshot the prior value and fade it out over the
  // freshly-swapped live number, dissolving to reveal it. Same-family growth
  // does NOT snapshot — the live layer persists and only the new leading column
  // fades in.
  // eslint-disable-next-line solid/reactivity -- `display` is invoked inside the hook's createEffect (a tracked scope); passing the accessor is intended.
  const exiting = useFadingSnapshots(display, {
    shouldSnapshot: (prev, next) => shapeFamily(prev) !== shapeFamily(next),
    durationMs: styles.SWAP_MS,
  })

  return (
    <span class={styles.root}>
      {/* Real value for assistive tech and tests; the visual odometer is aria-hidden. */}
      <span class={styles.srOnly}>{`${display()} tokens`}</span>
      <span class={styles.numberBox}>
        {/* In-flow, hidden: owns the number's width and baseline. */}
        <span class={styles.numberGhost} aria-hidden="true">{display()}</span>
        <For each={familyKey()}>
          {() => (
            <span class={styles.liveLayer} aria-hidden="true">
              <RollingNumber value={display()} animateEntry={true} />
            </span>
          )}
        </For>
        <For each={exiting}>
          {snap => (
            <span class={styles.exitingLayer} data-testid="odo-exiting" aria-hidden="true">
              <RollingNumber value={snap.value} animateEntry={false} />
            </span>
          )}
        </For>
      </span>
      {' tokens'}
    </span>
  )
}
