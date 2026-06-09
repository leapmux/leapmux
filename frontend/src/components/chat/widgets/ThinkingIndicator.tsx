import type { Component, JSX } from 'solid-js'
import { createEffect, createMemo, createSignal, For, onCleanup, onMount, Show, untrack } from 'solid-js'
import { createCompassSimulation } from '../compassPhysics'
import { getRandomVerb } from '../spinnerVerbs'
import * as styles from './ThinkingIndicator.css'
import { ThinkingTokenCount } from './ThinkingTokenCount'

export interface ThinkingIndicatorProps {
  visible: boolean
  /**
   * When true, the compass simulation is suspended (no setInterval, no DOM
   * writes). Used to skip animation work for ChatViews that are mounted but
   * not the active tab in their tile. Visibility/expand state is unaffected
   * so the indicator remains correctly expanded when the user switches in.
   */
  paused?: boolean
  onExpandTick?: () => void
  /**
   * Stable identifier (typically the agent id) used to persist the
   * randomly-chosen verb across re-mounts of this component. Without
   * it, every mount picks a fresh verb — so a tile split / make-grid
   * / close-grid that re-mounts ChatView mid-stream would flip the
   * verb visibly. When supplied, the verb persists across re-mounts
   * during a continuous thinking session and only refreshes on
   * genuine idle→thinking transitions inside a live component (and
   * on the 60s in-turn rotation, with a crossfade).
   */
  id?: string
  /**
   * Running estimate of the in-flight turn's thinking (reasoning) tokens,
   * surfaced as a small count beside the verb. Broadcast-only telemetry that
   * is cleared at turn boundaries; rendered only while positive so an idle
   * indicator shows nothing.
   */
  thinkingTokens?: number
}

// How often the verb rotates while the indicator is visible (and not
// paused). Long enough that any individual verb gets noticed but short
// enough that a multi-minute turn surfaces several.
const ROTATION_INTERVAL_MS = 60_000
// CSS opacity transition duration on the verb spans. Kept in lockstep
// with the value in ThinkingIndicator.css — rotation logic uses it to
// know when the now-inactive span has finished fading out, so the dead
// content can be cleared without affecting the grid cell width during
// the fade itself.
const ROTATION_FADE_MS = 500
// The wrapper's opacity-fade duration when the indicator collapses (the 0.3s
// `opacity` transition in the render style below). The token count holds its
// last value mounted for this long after the gate closes so it fades out WITH
// the collapsing row instead of popping; see `countTokens`.
const ROW_FADE_MS = 300

// Module-level cache of the indicator's persistent state per id —
// the verb currently displayed and the last compass angle (in
// radians). Survives ThinkingIndicator re-mounts caused by
// layout-tree restructures (tile split / make-grid / close-grid) so
// neither the verb nor the pendulum visibly snaps when the
// indicator's DOM re-mounts mid-stream.
//
// Entries are written on mount (when seeding the verb), on
// invisible→visible transitions inside a live component, on each 60s
// in-turn verb rotation, and on every sim tick for the angle.
// Updating an existing key is in-place — the map's insertion-order
// queue isn't disturbed — so only first-seen ids advance the FIFO.
//
// Size is bounded by MAX_CACHE_ENTRIES with FIFO eviction. Eviction
// kicks in only when a NEW id arrives past the cap; an evicted
// agent's next re-mount simply falls back to a fresh verb / zero
// angle, which is the same behaviour as the very first mount of any
// id. There's no explicit "agent closed" hook because the cap
// catches it within a bounded number of subsequent agent opens
// regardless.
interface IndicatorSnapshot {
  verb?: string
  angleRad?: number
}
const MAX_CACHE_ENTRIES = 128
const indicatorCache = new Map<string, IndicatorSnapshot>()

function trimCache(): void {
  // Map iteration is insertion order. Drop the oldest until we're
  // back under the cap. The loop handles arbitrary overshoot
  // (single insert can only push one over the cap, but a future
  // batch-set path is safe by construction).
  while (indicatorCache.size > MAX_CACHE_ENTRIES) {
    const oldest = indicatorCache.keys().next().value
    if (oldest === undefined)
      break
    indicatorCache.delete(oldest)
  }
}

// Merge a patch into id's cached snapshot: mutate the existing entry in place
// (so the FIFO insertion order isn't disturbed and only first-seen ids advance
// the eviction queue), or insert + trim for a new id. cacheVerb/cacheAngle are
// thin single-field wrappers so call sites read as intent ("cache the verb")
// and an accidental multi-key patch can't clobber an unrelated cached field.
function cacheSnapshot(id: string | undefined, patch: Partial<IndicatorSnapshot>): void {
  if (id === undefined)
    return
  const existing = indicatorCache.get(id)
  if (existing) {
    Object.assign(existing, patch)
    return
  }
  indicatorCache.set(id, { ...patch })
  trimCache()
}

function cacheVerb(id: string | undefined, verb: string): void {
  cacheSnapshot(id, { verb })
}

function cacheAngle(id: string | undefined, angleRad: number): void {
  cacheSnapshot(id, { angleRad })
}

function pickAndCacheVerb(id: string | undefined): string {
  const v = getRandomVerb()
  cacheVerb(id, v)
  return v
}

export const ThinkingIndicator: Component<ThinkingIndicatorProps> = (props) => {
  // eslint-disable-next-line solid/reactivity -- intentional setup-time read; the effect below tracks reactive updates.
  const initialId = props.id
  const snapshot = initialId !== undefined ? indicatorCache.get(initialId) : undefined
  const cached = snapshot?.verb
  // Two stacked verb slots so we can crossfade between them. The active
  // one renders at opacity 1, the other at opacity 0 — when rotating,
  // we put the new verb in the inactive slot and flip `activeIsA`,
  // letting CSS opacity transitions handle the visual swap. The
  // inactive slot is cleared after the fade completes so its (now-
  // stale) content doesn't keep widening the grid cell on subsequent
  // rotations.
  const [verbA, setVerbA] = createSignal(cached ?? pickAndCacheVerb(initialId))
  const [verbB, setVerbB] = createSignal('')
  const [activeIsA, setActiveIsA] = createSignal(true)

  const charsA = createMemo(() => (verbA() ? `${verbA()}...`.split('') : []))
  const charsB = createMemo(() => (verbB() ? `${verbB()}...`.split('') : []))

  // Seed the angle from the cache so a re-mount caused by a layout
  // change resumes the pendulum at the angle the user last saw,
  // instead of snapping back to 0. The simulation itself is given the
  // same seed (in radians) below.
  const seedAngleRad = snapshot?.angleRad ?? 0
  const [angleDeg, setAngleDeg] = createSignal((seedAngleRad * 180) / Math.PI)
  // Wave position is derived per-verb from the compass angle and that
  // verb's own char count. Computing it per-verb (rather than sharing
  // a single highlightPos signal) means verbs of different lengths
  // still render the wave at the same RELATIVE position — the wave
  // doesn't visually "jump" when a long verb crossfades to a short
  // one.
  const computeHighlight = (count: number): number => {
    if (count <= 0)
      return 0
    const pos = (angleDeg() / 360) * count
    return ((pos % count) + count) % count
  }
  const highlightPosA = createMemo(() => computeHighlight(charsA().length))
  const highlightPosB = createMemo(() => computeHighlight(charsB().length))

  // If we mount with visible=true — typically because the host
  // component (ChatView) was just re-mounted as part of a layout-tree
  // restructure like a tile split, while the agent is still actively
  // thinking — seed expanded=true so the indicator appears
  // already-open. CSS transitions don't fire on the initial render
  // value, so this skips the 300ms expand animation that would
  // otherwise look like a disappear/reappear flicker. Fresh mounts
  // with the agent idle still start collapsed.
  //
  // Reading props.visible here at setup time is intentional: we want
  // the value as of mount, not a reactive subscription (the effect
  // below tracks subsequent changes). The eslint rule is conservative
  // about prop reads outside tracked scopes — this case is one of the
  // legitimate exceptions it documents.
  // eslint-disable-next-line solid/reactivity
  const initiallyVisible = !!props.visible
  const [expanded, setExpanded] = createSignal(initiallyVisible)

  const sim = createCompassSimulation((state) => {
    setAngleDeg((state.angle * 180) / Math.PI)
    cacheAngle(props.id, state.angle)
  }, seedAngleRad)

  let expandRafId = 0
  let tickRafId = 0
  let rotateIntervalId: ReturnType<typeof setInterval> | undefined
  const pendingClearTimers = new Set<ReturnType<typeof setTimeout>>()
  let wasVisible = initiallyVisible

  // The token count's mounted value, decoupled from the live estimate so it can
  // fade out WITH the collapsing row instead of popping. While the count should
  // show (indicator visible + a positive estimate) it tracks the live value; when
  // the gate closes it freezes the last value mounted for ROW_FADE_MS (the
  // wrapper's opacity fade) and then unmounts. The frozen value means no roll
  // effects fire during the fade.
  const [countTokens, setCountTokens] = createSignal<number | undefined>(undefined)
  let countFadeTimer: ReturnType<typeof setTimeout> | undefined
  createEffect(() => {
    const tokens = props.thinkingTokens
    if (props.visible && (tokens ?? 0) > 0) {
      if (countFadeTimer !== undefined) {
        clearTimeout(countFadeTimer)
        countFadeTimer = undefined
      }
      setCountTokens(tokens)
      return
    }
    // Gate closed: keep the last value mounted through the wrapper's opacity
    // fade, then unmount. untrack the read so this effect doesn't depend on its
    // own write (it tracks only props.visible / props.thinkingTokens).
    if (untrack(countTokens) !== undefined && countFadeTimer === undefined) {
      countFadeTimer = setTimeout(() => {
        countFadeTimer = undefined
        setCountTokens(undefined)
      }, ROW_FADE_MS)
    }
  })

  // Drive `onExpandTick` for ~700ms so the parent's scroll-sticky
  // binding can re-pin to the bottom on every frame while the
  // indicator's height settles. Used both for the false→true expand
  // animation (where the row grows over 300ms) and for the
  // initiallyVisible mount case (where there's no animation, but the
  // freshly-laid-out indicator's SVG / font load can still nudge
  // scrollHeight up after the initial paint and leave a re-mounted
  // ChatView scrolled just above the indicator).
  const runExpandTickLoop = () => {
    const start = performance.now()
    const tick = () => {
      props.onExpandTick?.()
      if (performance.now() - start < 700) {
        tickRafId = requestAnimationFrame(tick)
      }
    }
    tickRafId = requestAnimationFrame(tick)
  }

  // Rotate to a fresh verb. Puts the new verb in the currently-
  // inactive slot, flips activeIsA so CSS crossfades opacity, caches
  // the new verb so a layout-induced re-mount pins to the latest
  // (not a stale earlier verb), and schedules a cleanup that clears
  // the now-inactive slot once the fade finishes. The clear is what
  // keeps the grid cell from staying perma-wide after a long verb
  // gives way to a shorter one.
  const rotateVerb = () => {
    // getRandomVerb already avoids repeating the most-recently-returned
    // verb globally; the safety check here defends against the
    // single-verb-pool edge case where it returns the same string.
    const next = getRandomVerb()
    const current = activeIsA() ? verbA() : verbB()
    if (next === current)
      return

    if (activeIsA()) {
      setVerbB(next)
      setActiveIsA(false)
      const t = setTimeout(() => {
        pendingClearTimers.delete(t)
        if (!activeIsA())
          setVerbA('')
      }, ROTATION_FADE_MS)
      pendingClearTimers.add(t)
    }
    else {
      setVerbA(next)
      setActiveIsA(true)
      const t = setTimeout(() => {
        pendingClearTimers.delete(t)
        if (activeIsA())
          setVerbB('')
      }, ROTATION_FADE_MS)
      pendingClearTimers.add(t)
    }

    // Updates only the verb field of the snapshot, leaving angleRad
    // untouched — the sim is still ticking through the rotation and
    // must not be reset to 0.
    cacheVerb(props.id, next)
  }

  // Initiallly-visible mount path: skip the height-expand animation
  // (it would look like a disappear/reappear when ChatView re-mounts
  // mid-stream via a tile split), but still tick onExpandTick so the
  // parent re-pins to bottom as the indicator's content lays out.
  onMount(() => {
    if (initiallyVisible)
      runExpandTickLoop()
  })

  createEffect(() => {
    const visible = props.visible
    const paused = props.paused ?? false

    if (visible) {
      if (!wasVisible) {
        wasVisible = true
        // Genuine idle→thinking transition inside a live component:
        // start a fresh turn with a fresh verb in slot A. We don't
        // crossfade here — the whole wrapper is fading in from
        // opacity 0 already, and the old slot-B content (if any)
        // would be a stale verb from the prior turn that we don't
        // want to flash through.
        setVerbA(pickAndCacheVerb(props.id))
        setVerbB('')
        setActiveIsA(true)
        expandRafId = requestAnimationFrame(() => setExpanded(true))
        // Notify parent on each frame during the height transition so it can
        // keep the scroll position pinned to the bottom.
        runExpandTickLoop()
      }
    }
    else {
      wasVisible = false
      cancelAnimationFrame(expandRafId)
      cancelAnimationFrame(tickRafId)
      setExpanded(false)
    }

    // Compass simulation + in-turn verb rotation are both gated on
    // visible-and-not-paused: a background tab shouldn't burn CPU on
    // animation, and a hidden indicator rotating its verb every 60s
    // wastes work on something the user can't see.
    if (visible && !paused) {
      sim.start()
      if (rotateIntervalId === undefined)
        rotateIntervalId = setInterval(rotateVerb, ROTATION_INTERVAL_MS)
    }
    else {
      sim.stop()
      if (rotateIntervalId !== undefined) {
        clearInterval(rotateIntervalId)
        rotateIntervalId = undefined
      }
    }
  })

  onCleanup(() => {
    cancelAnimationFrame(expandRafId)
    cancelAnimationFrame(tickRafId)
    sim.stop()
    if (rotateIntervalId !== undefined) {
      clearInterval(rotateIntervalId)
      rotateIntervalId = undefined
    }
    for (const t of pendingClearTimers)
      clearTimeout(t)
    pendingClearTimers.clear()
    if (countFadeTimer !== undefined)
      clearTimeout(countFadeTimer)
  })

  const verbSpan = (
    isSlotA: boolean,
    verbChars: () => string[],
    verbHighlight: () => number,
  ) => (
    <span
      class={styles.verb}
      classList={{ [styles.verbActive]: activeIsA() === isSlotA }}
      style={{
        '--highlight-pos': String(verbHighlight()),
        '--char-total': String(verbChars().length || 1),
      } as JSX.CSSProperties}
    >
      <For each={verbChars()}>
        {(char, i) => (
          <span
            class={styles.char}
            style={{ '--char-i': String(i()) } as JSX.CSSProperties}
          >
            {char}
          </span>
        )}
      </For>
    </span>
  )

  return (
    <div
      class={styles.wrapper}
      data-testid="thinking-indicator"
      style={{
        'grid-template-rows': expanded() ? '1fr' : '0fr',
        'opacity': expanded() ? 1 : 0,
        'transition': expanded()
          ? 'grid-template-rows 0.3s ease-out, opacity 0.3s ease-out 0.3s'
          : 'opacity 0.3s ease-out, grid-template-rows 0.3s ease-out 0.3s',
      }}
    >
      <div class={styles.wrapperInner}>
        <div class={styles.container}>
          <svg class={styles.compass} viewBox="0 0 401.294 401.294">
            <g transform={`translate(100.666,-852.275) rotate(${angleDeg()},100,1052.922)`}>
              {/* Tertiary intercardinal points */}
              <g transform="matrix(0.41544,-0.17208,0.17208,0.41544,-122.740,632.706)">
                <path fill="currentColor" stroke="currentColor" stroke-width="2.224" d="m100,852.362-30,170 30,30 0-200z" />
                <path fill="var(--background)" stroke="currentColor" stroke-width="2.224" d="m99.962,852.362 30,170-30,30 0-200z" />
                <path fill="currentColor" stroke="currentColor" stroke-width="2.224" d="m99.962,1253.482 30-170-30-30 0,200z" />
                <path fill="var(--background)" stroke="currentColor" stroke-width="2.224" d="m100,1253.482-30-170 30-30 0,200z" />
                <path fill="currentColor" stroke="currentColor" stroke-width="2.224" d="m300.541,1052.941-170-30-30,30 200,0z" />
                <path fill="var(--background)" stroke="currentColor" stroke-width="2.224" d="m300.541,1052.904-170,30-30-30 200,0z" />
                <path fill="currentColor" stroke="currentColor" stroke-width="2.224" d="m-100.579,1052.904 170,30 30-30-200,0z" />
                <path fill="var(--background)" stroke="currentColor" stroke-width="2.224" d="m-100.579,1052.941 170-30 30,30-200,0z" />
              </g>
              {/* Secondary intercardinal points */}
              <g transform="matrix(0.17208,-0.41544,0.41544,0.17208,-354.645,913.272)">
                <path fill="currentColor" stroke="currentColor" stroke-width="2.224" d="m100,852.362-30,170 30,30 0-200z" />
                <path fill="var(--background)" stroke="currentColor" stroke-width="2.224" d="m99.962,852.362 30,170-30,30 0-200z" />
                <path fill="currentColor" stroke="currentColor" stroke-width="2.224" d="m99.962,1253.482 30-170-30-30 0,200z" />
                <path fill="var(--background)" stroke="currentColor" stroke-width="2.224" d="m100,1253.482-30-170 30-30 0,200z" />
                <path fill="currentColor" stroke="currentColor" stroke-width="2.224" d="m300.541,1052.941-170-30-30,30 200,0z" />
                <path fill="var(--background)" stroke="currentColor" stroke-width="2.224" d="m300.541,1052.904-170,30-30-30 200,0z" />
                <path fill="currentColor" stroke="currentColor" stroke-width="2.224" d="m-100.579,1052.904 170,30 30-30-200,0z" />
                <path fill="var(--background)" stroke="currentColor" stroke-width="2.224" d="m-100.579,1052.941 170-30 30,30-200,0z" />
              </g>
              {/* Annulus ring */}
              <path fill="currentColor" stroke="currentColor" stroke-width="1" transform="translate(0,852.362)" d="M100,37.15A162.85,162.85 0 0 0-62.85,200 162.85,162.85 0 0 0 100,362.85 162.85,162.85 0 0 0 262.85,200 162.85,162.85 0 0 0 100,37.15zM100,65.5A134.5,134.5 0 0 1 234.5,200 134.5,134.5 0 0 1 100,334.5 134.5,134.5 0 0 1-34.5,200 134.5,134.5 0 0 1 100,65.5z" />
              {/* Intermediate intercardinal points (NE, SE, SW, NW) */}
              <g>
                <path fill="currentColor" stroke="currentColor" d="m185.055,967.864-84.828,59.38 0,25.448 84.828-84.828z" />
                <path fill="var(--background)" stroke="currentColor" d="m185.039,967.848-59.38,84.828-25.448,0 84.828-84.828z" />
                <path fill="currentColor" stroke="currentColor" d="m14.907,1137.98 84.829-59.38 0-25.448-84.829,84.828z" />
                <path fill="var(--background)" stroke="currentColor" d="m14.923,1137.996 59.38-84.828 25.448,0-84.828,84.828z" />
                <path fill="currentColor" stroke="currentColor" d="m185.039,1137.996-59.38-84.828-25.448,0 84.828,84.828z" />
                <path fill="var(--background)" stroke="currentColor" d="m185.055,1137.98-84.828-59.38 0-25.448 84.828,84.828z" />
                <path fill="currentColor" stroke="currentColor" d="m14.923,967.848 59.38,84.828 25.448,0-84.828-84.828z" />
                <path fill="var(--background)" stroke="currentColor" d="m14.907,967.864 84.829,59.38 0,25.448-84.829-84.828z" />
              </g>
              {/* Cardinal points (N, S, E, W) */}
              <g>
                <path fill="currentColor" stroke="currentColor" d="m100,852.362-30,170 30,30 0-200z" />
                <path fill="var(--background)" stroke="currentColor" d="m99.962,852.362 30,170-30,30 0-200z" />
                <path fill="currentColor" stroke="currentColor" d="m99.962,1253.482 30-170-30-30 0,200z" />
                <path fill="var(--background)" stroke="currentColor" d="m100,1253.482-30-170 30-30 0,200z" />
                <path fill="currentColor" stroke="currentColor" d="m300.541,1052.941-170-30-30,30 200,0z" />
                <path fill="var(--background)" stroke="currentColor" d="m300.541,1052.904-170,30-30-30 200,0z" />
                <path fill="currentColor" stroke="currentColor" d="m-100.579,1052.904 170,30 30-30-200,0z" />
                <path fill="var(--background)" stroke="currentColor" d="m-100.579,1052.941 170-30 30,30-200,0z" />
              </g>
            </g>
          </svg>
          <span class={styles.verbRow}>
            <span class={styles.verbStack}>
              {/* Stable baseline anchor — see baselineStrut in the CSS. */}
              <span class={styles.baselineStrut} aria-hidden="true">{' '}</span>
              {verbSpan(true, charsA, highlightPosA)}
              {verbSpan(false, charsB, highlightPosB)}
            </span>
            {/* `countTokens` (not the raw prop) gates the count: it tracks the
                live estimate while the indicator is visible, then holds the last
                value for ROW_FADE_MS so the count fades out WITH the collapsing
                row instead of popping — and unmounts after, so a stale estimate
                can't keep it (or its roll effects) alive in a collapsed row. */}
            <Show when={countTokens() !== undefined}>
              <ThinkingTokenCount tokens={countTokens()!} />
            </Show>
          </span>
        </div>
      </div>
    </div>
  )
}
