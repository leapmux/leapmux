import type { Component } from 'solid-js'
import { createEffect, createSignal, For, onCleanup, Show } from 'solid-js'
import { createCompassSimulation } from './compassPhysics'
import { getRandomVerb } from './spinnerVerbs'
import * as styles from './ThinkingIndicator.css'

function charWeight(index: number, total: number, highlightPos: number): number {
  const dist = Math.abs(index - highlightPos)
  const wrappedDist = Math.min(dist, total - dist)
  const falloff = Math.max(0, 1 - (wrappedDist / total) * 2)
  return 400 + 300 * falloff
}

export interface ThinkingIndicatorProps {
  visible: boolean
}

export const ThinkingIndicator: Component<ThinkingIndicatorProps> = (props) => {
  const text = `${getRandomVerb()}...`
  const chars = text.split('')
  const [angleDeg, setAngleDeg] = createSignal(0)
  const [highlightPos, setHighlightPos] = createSignal(0)
  // eslint-disable-next-line solid/reactivity -- initial value only
  const [mounted, setMounted] = createSignal(props.visible)

  const sim = createCompassSimulation((state) => {
    setAngleDeg((state.angle * 180) / Math.PI)
    const pos = ((-state.angle * 180) / Math.PI / 360) * chars.length
    setHighlightPos(((pos % chars.length) + chars.length) % chars.length)
  })

  createEffect(() => {
    if (props.visible) {
      setMounted(true)
      sim.start()
    }
    else {
      sim.stop()
    }
  })

  onCleanup(() => sim.stop())

  const onTransitionEnd = () => {
    if (!props.visible) {
      setMounted(false)
    }
  }

  return (
    <Show when={mounted()}>
      <div
        class={styles.container}
        data-testid="thinking-indicator"
        style={{ opacity: props.visible ? 1 : 0 }}
        onTransitionEnd={onTransitionEnd}
      >
        <svg class={styles.compass} viewBox="-248 -248 496 496">
          <g transform={`rotate(${angleDeg()})`}>
            <circle r="120" fill="none" stroke="currentColor" />
            <g id="ic2" transform="rotate(22.5)">
              <path fill="currentColor" stroke="currentColor" d="M33,0 120,120 0,0" opacity="0.29" />
              <path fill="currentColor" stroke="currentColor" d="M0,33 120,120 0,0" opacity="0.12" />
            </g>
            <use href="#ic2" transform="rotate(45)" />
            <use href="#ic2" transform="rotate(90)" />
            <use href="#ic2" transform="rotate(135)" />
            <use href="#ic2" transform="rotate(180)" />
            <use href="#ic2" transform="rotate(225)" />
            <use href="#ic2" transform="rotate(270)" />
            <use href="#ic2" transform="rotate(315)" />
            <g id="ic">
              <path fill="currentColor" stroke="currentColor" d="M48,0 171,171 0,0" opacity="0.4" />
              <path fill="currentColor" stroke="currentColor" d="M0,48 171,171 0,0" opacity="0.24" />
            </g>
            <use href="#ic" transform="rotate(90)" />
            <use href="#ic" transform="rotate(180)" />
            <use href="#ic" transform="rotate(270)" />
            <g id="c" stroke="currentColor">
              <path fill="currentColor" d="M0,0 34,-34 244,0" />
              <path fill="none" d="M0,0 34,34 244,0" />
            </g>
            <use href="#c" transform="rotate(90)" />
            <use href="#c" transform="rotate(180)" />
            <use href="#c" transform="rotate(270)" />
          </g>
        </svg>
        <span class={styles.verb}>
          <For each={chars}>
            {(char, i) => (
              <span style={{ 'font-weight': charWeight(i(), chars.length, highlightPos()) }}>
                {char}
              </span>
            )}
          </For>
        </span>
      </div>
    </Show>
  )
}
