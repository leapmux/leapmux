import { createEffect, createMemo, createSignal, For, onCleanup, onMount, Show } from 'solid-js'
import { createKeyedElementRefs } from '~/lib/keyedElementRefs'
import { createRafResizeObserver } from '~/lib/resizeObserver'
import { nextRovingValue } from '~/lib/rovingFocus'
import { sameValueZero, shallowEqualMapKeyArrays } from '~/lib/shallowEqual'
import * as styles from './PillGroup.css'
import { Tooltip } from './Tooltip'

/** One choice in a {@link PillGroup}. */
export interface PillOptionSpec<K> {
  /** The unique selection key. */
  key: K
  label: string
  /** A non-empty reason that makes this option unavailable. */
  disabledReason?: string
}

/** One through four fixed choices. Use a menu for any other list. */
export type PillOptions<K>
  = | readonly [PillOptionSpec<K>]
    | readonly [PillOptionSpec<K>, PillOptionSpec<K>]
    | readonly [PillOptionSpec<K>, PillOptionSpec<K>, PillOptionSpec<K>]
    | readonly [PillOptionSpec<K>, PillOptionSpec<K>, PillOptionSpec<K>, PillOptionSpec<K>]

export const PILL_OPTION_LIMIT = 4

type PillOptionState
  = | { kind: 'enabled' }
    | { kind: 'option-refused', reason: string }
    | { kind: 'group-refused' }

const ENABLED: PillOptionState = { kind: 'enabled' }
const GROUP_REFUSED: PillOptionState = { kind: 'group-refused' }

interface SelectionMetrics {
  left: number
  right: number
}

/** One radio in the group. */
function PillOption(props: {
  selected: boolean
  selectionOverlayReady: boolean
  selectionSettled: boolean
  tabStop: boolean
  state: PillOptionState
  separated: boolean
  onClick: () => void
  onFocus: () => void
  ref: (element: HTMLButtonElement) => void
  children: string
}) {
  const optionRefused = () => props.state.kind === 'option-refused'
  const groupRefused = () => props.state.kind === 'group-refused'
  const pill = () => (
    <button
      type="button"
      class={styles.pillOption}
      classList={{
        [styles.pillOptionActive]: props.selected && !props.selectionOverlayReady,
        [styles.pillOptionSelectedTarget]: props.selected
          && props.selectionOverlayReady
          && props.selectionSettled,
        [styles.pillOptionDimmed]: optionRefused() && !props.selected,
        [styles.pillOptionSeparated]: props.separated,
        [styles.pillOptionUnavailable]: optionRefused(),
      }}
      role="radio"
      aria-checked={props.selected}
      aria-disabled={optionRefused() ? 'true' : undefined}
      disabled={groupRefused()}
      tabIndex={props.tabStop && !groupRefused() ? 0 : -1}
      ref={props.ref}
      onClick={props.onClick}
      onFocus={props.onFocus}
    >
      {props.children}
    </button>
  )

  return (
    <Show
      when={props.state.kind === 'option-refused' ? props.state.reason : undefined}
      fallback={pill()}
    >
      {reason => <Tooltip text={reason()}>{pill()}</Tooltip>}
    </Show>
  )
}

function optionMap<K>(label: string, options: readonly PillOptionSpec<K>[]): Map<K, PillOptionSpec<K>> {
  if (options.length < 1 || options.length > PILL_OPTION_LIMIT)
    throw new RangeError(`PillGroup "${label}" requires one through four options.`)
  const byKey = new Map<K, PillOptionSpec<K>>()
  for (const option of options) {
    if (byKey.has(option.key))
      throw new TypeError(`PillGroup "${label}" requires unique option keys.`)
    if (option.disabledReason !== undefined && option.disabledReason.trim() === '')
      throw new TypeError(`PillGroup "${label}" requires a non-empty disabled reason.`)
    byKey.set(option.key, option)
  }
  return byKey
}

/** A segmented one-of-N control with radio semantics. */
export function PillGroup<K>(props: {
  label: string
  options: PillOptions<K>
  selectedKey: K
  onSelect: (key: K) => void
  /** Show the current selection and refuse all changes. */
  disabled?: boolean
}) {
  const optionsByKey = createMemo(() => optionMap(props.label, props.options))
  const optionKeys = createMemo(
    () => [...optionsByKey().keys()],
    [],
    { equals: shallowEqualMapKeyArrays },
  )
  const optionEls = createKeyedElementRefs<K, HTMLButtonElement>()
  const [focusedKey, setFocusedKey] = createSignal<{ value: K }>()
  const [selectionMetrics, setSelectionMetrics] = createSignal<SelectionMetrics>()
  const [selectionMoves, setSelectionMoves] = createSignal(false)
  const [selectionSettled, setSelectionSettled] = createSignal(true)
  let groupEl: HTMLDivElement | undefined
  let selectionFillEl: HTMLSpanElement | undefined
  let resizeObserver: ReturnType<typeof createRafResizeObserver>
  let geometryObserver: MutationObserver | undefined
  let hasSelectionMetrics = false
  let selectionMotionGeneration = 0

  const settleSelectionAfterMotion = () => {
    const generation = ++selectionMotionGeneration
    requestAnimationFrame(() => {
      if (generation !== selectionMotionGeneration)
        return
      const animations = selectionFillEl?.getAnimations?.() ?? []
      if (animations.length === 0) {
        setSelectionSettled(true)
        return
      }
      void Promise.allSettled(animations.map(animation => animation.finished)).then(() => {
        if (generation === selectionMotionGeneration)
          setSelectionSettled(true)
      })
    })
  }

  const stateFor = (option: PillOptionSpec<K>): PillOptionState => {
    if (props.disabled === true)
      return GROUP_REFUSED
    if (option.disabledReason !== undefined)
      return { kind: 'option-refused', reason: option.disabledReason }
    return ENABLED
  }

  const focusableOptions = () => [...optionsByKey().values()]
    .filter(option => stateFor(option).kind !== 'group-refused')

  const measureSelection = (move: boolean) => {
    const selected = optionsByKey().get(props.selectedKey)
    const selectedEl = selected === undefined ? undefined : optionEls.get(selected.key)
    if (!groupEl || !selectedEl?.isConnected || groupEl.clientWidth <= 0) {
      selectionMotionGeneration++
      hasSelectionMetrics = false
      setSelectionMoves(false)
      setSelectionSettled(true)
      setSelectionMetrics(undefined)
      return
    }

    const groupRect = groupEl.getBoundingClientRect()
    const selectedRect = selectedEl.getBoundingClientRect()
    const layoutWidth = Number.parseFloat(getComputedStyle(groupEl).width)
    const validViewportGeometry = groupRect.width > 0
      && selectedRect.width > 0
      && Number.isFinite(layoutWidth)
      && layoutWidth > 0
    const scaleX = validViewportGeometry ? groupRect.width / layoutWidth : 1
    const rawLeft = validViewportGeometry
      ? (selectedRect.left - groupRect.left) / scaleX - groupEl.clientLeft + groupEl.scrollLeft
      : selectedEl.offsetLeft
    const rawWidth = validViewportGeometry ? selectedRect.width / scaleX : selectedEl.offsetWidth
    const left = Math.max(0, rawLeft)
    const availableWidth = Math.max(0, groupEl.clientWidth - left)
    const width = Math.min(Math.max(0, rawWidth), availableWidth)
    const right = Math.max(0, groupEl.clientWidth - left - width)

    const selectionChanged = move && hasSelectionMetrics
    const startsMotion = selectionChanged
      && (typeof matchMedia !== 'function' || !matchMedia('(prefers-reduced-motion: reduce)').matches)
    if (selectionChanged)
      setSelectionMoves(true)
    if (startsMotion) {
      setSelectionSettled(false)
    }
    else if (selectionChanged) {
      setSelectionSettled(true)
    }
    else if (!hasSelectionMetrics) {
      setSelectionMoves(false)
      setSelectionSettled(true)
    }
    hasSelectionMetrics = true
    setSelectionMetrics({ left, right })
    if (startsMotion || !selectionSettled())
      settleSelectionAfterMotion()
  }

  const tabOwner = createMemo<{ value: K } | undefined>(() => {
    const focused = focusedKey()
    if (focused !== undefined) {
      const option = optionsByKey().get(focused.value)
      if (option !== undefined && stateFor(option).kind !== 'group-refused')
        return focused
    }

    const selected = optionsByKey().get(props.selectedKey)
    if (selected !== undefined && stateFor(selected).kind !== 'group-refused')
      return { value: selected.key }

    const first = focusableOptions()[0]
    return first === undefined ? undefined : { value: first.key }
  })

  const focus = (key: K) => {
    optionEls.get(key)?.focus()
  }

  const ownsTabStop = (key: K) => {
    const owner = tabOwner()
    return owner !== undefined && sameValueZero(key, owner.value)
  }

  const select = (key: K) => {
    const option = optionsByKey().get(key)
    if (option === undefined)
      return
    focus(key)
    if (stateFor(option).kind !== 'enabled' || sameValueZero(key, props.selectedKey))
      return
    props.onSelect(key)
  }

  const onKeyDown = (event: KeyboardEvent) => {
    const focusable = focusableOptions()
    const owner = tabOwner()
    if (owner === undefined)
      return
    const next = nextRovingValue(focusable.map(option => option.key), owner.value, event)
    if (next === undefined)
      return
    event.preventDefault()
    select(next.value)
  }

  const onFocusOut = (event: FocusEvent) => {
    const destination = event.relatedTarget
    if (!(destination instanceof Node) || !groupEl?.contains(destination))
      setFocusedKey(undefined)
  }

  const restoreFocusAfterRemoval = (element: HTMLButtonElement) => {
    onCleanup(() => {
      if (document.activeElement !== element)
        return
      setFocusedKey(undefined)
      queueMicrotask(() => {
        if (!groupEl?.isConnected)
          return
        const active = document.activeElement
        if (active !== document.body && active !== document.documentElement)
          return
        const owner = tabOwner()
        if (owner !== undefined)
          focus(owner.value)
      })
    })
  }

  let previousSelection: { value: K } | undefined
  createEffect(() => {
    const selected = optionsByKey().get(props.selectedKey)
    const current = selected === undefined ? undefined : { value: selected.key }
    const move = previousSelection !== undefined
      && current !== undefined
      && !sameValueZero(previousSelection.value, current.value)
    previousSelection = current
    measureSelection(move)
  })

  onMount(() => {
    resizeObserver = createRafResizeObserver(() => measureSelection(false))
    if (groupEl)
      resizeObserver?.observe(groupEl)
    for (const key of optionKeys()) {
      const element = optionEls.get(key)
      if (element)
        resizeObserver?.observe(element)
    }

    if (groupEl && typeof MutationObserver !== 'undefined') {
      geometryObserver = new MutationObserver(() => measureSelection(false))
      for (let element: HTMLElement | null = groupEl; element; element = element.parentElement) {
        geometryObserver.observe(element, { attributes: true })
      }
    }
    measureSelection(false)
  })

  onCleanup(() => {
    selectionMotionGeneration++
    resizeObserver?.disconnect()
    geometryObserver?.disconnect()
  })

  const selectionStyle = (metrics: SelectionMetrics) => ({
    '--pill-selection-left': `${metrics.left}px`,
    '--pill-selection-right': `${metrics.right}px`,
  })

  return (
    <div
      ref={(element) => {
        groupEl = element
        onCleanup(() => {
          if (groupEl === element)
            groupEl = undefined
        })
      }}
      class={styles.pillGroup}
      classList={{ [styles.pillGroupDisabled]: props.disabled === true }}
      role="radiogroup"
      aria-label={props.label}
      onKeyDown={onKeyDown}
      onFocusOut={onFocusOut}
    >
      <Show when={selectionMetrics()}>
        {metrics => (
          <>
            <span
              data-pill-selection-fill
              ref={(element) => {
                selectionFillEl = element
                onCleanup(() => {
                  if (selectionFillEl === element)
                    selectionFillEl = undefined
                })
              }}
              class={styles.selectionFill}
              classList={{ [styles.selectionWindowMoves]: selectionMoves() }}
              aria-hidden="true"
              style={selectionStyle(metrics())}
            />
            <div
              data-pill-selection-labels
              class={styles.selectionLabels}
              classList={{ [styles.selectionWindowMoves]: selectionMoves() }}
              aria-hidden="true"
              style={selectionStyle(metrics())}
            >
              <For each={optionKeys()}>
                {(key, index) => (
                  <Show when={optionsByKey().get(key)}>
                    {option => (
                      <span
                        class={styles.selectionLabel}
                        classList={{ [styles.pillOptionSeparated]: index() > 0 }}
                        data-label={option().label}
                      />
                    )}
                  </Show>
                )}
              </For>
            </div>
          </>
        )}
      </Show>
      <For each={optionKeys()}>
        {(key, index) => (
          <Show when={optionsByKey().get(key)}>
            {option => (
              <PillOption
                selected={sameValueZero(key, props.selectedKey)}
                selectionOverlayReady={selectionMetrics() !== undefined}
                selectionSettled={selectionSettled()}
                tabStop={ownsTabStop(key)}
                state={stateFor(option())}
                separated={index() > 0}
                onClick={() => select(key)}
                onFocus={() => setFocusedKey({ value: key })}
                ref={(element) => {
                  optionEls.register(key, element)
                  resizeObserver?.observe(element)
                  onCleanup(() => resizeObserver?.unobserve(element))
                  restoreFocusAfterRemoval(element)
                }}
              >
                {option().label}
              </PillOption>
            )}
          </Show>
        )}
      </For>
    </div>
  )
}
