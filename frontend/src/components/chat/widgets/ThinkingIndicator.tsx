import type { Component, JSX } from 'solid-js'
import { createEffect, createMemo, createSignal, For, onCleanup } from 'solid-js'
import { createCompassSimulation } from '../compassPhysics'
import { getRandomVerb } from '../spinnerVerbs'
import * as styles from './ThinkingIndicator.css'

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
}

export const ThinkingIndicator: Component<ThinkingIndicatorProps> = (props) => {
  const [verb, setVerb] = createSignal(getRandomVerb())
  const chars = createMemo(() => `${verb()}...`.split(''))
  const [angleDeg, setAngleDeg] = createSignal(0)
  const [highlightPos, setHighlightPos] = createSignal(0)
  const [expanded, setExpanded] = createSignal(false)

  const sim = createCompassSimulation((state) => {
    setAngleDeg((state.angle * 180) / Math.PI)
    const c = chars()
    const pos = ((state.angle * 180) / Math.PI / 360) * c.length
    setHighlightPos(((pos % c.length) + c.length) % c.length)
  })

  let expandRafId = 0
  let tickRafId = 0
  let wasVisible = false

  createEffect(() => {
    const visible = props.visible
    const paused = props.paused ?? false

    if (visible) {
      if (!wasVisible) {
        wasVisible = true
        setVerb(getRandomVerb())
        expandRafId = requestAnimationFrame(() => setExpanded(true))
        // Notify parent on each frame during the height transition so it can
        // keep the scroll position pinned to the bottom.
        const start = performance.now()
        const tick = () => {
          props.onExpandTick?.()
          if (performance.now() - start < 700) {
            tickRafId = requestAnimationFrame(tick)
          }
        }
        tickRafId = requestAnimationFrame(tick)
      }
    }
    else {
      wasVisible = false
      cancelAnimationFrame(expandRafId)
      cancelAnimationFrame(tickRafId)
      setExpanded(false)
    }

    if (visible && !paused)
      sim.start()
    else
      sim.stop()
  })

  onCleanup(() => {
    cancelAnimationFrame(expandRafId)
    cancelAnimationFrame(tickRafId)
    sim.stop()
  })

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
          <span
            class={styles.verb}
            style={{
              '--highlight-pos': String(highlightPos()),
              '--char-total': String(chars().length),
            } as JSX.CSSProperties}
          >
            <For each={chars()}>
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
        </div>
      </div>
    </div>
  )
}
