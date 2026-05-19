import type { Component, JSX } from 'solid-js'
import { createEffect, Match, Switch } from 'solid-js'

interface LoadingSelectProps {
  value: string
  onChange: (value: string) => void
  loading: boolean
  isEmpty: boolean
  loadingLabel: string
  emptyLabel: string
  /**
   * Caller-controlled disabled flag. OR'd with the internal disable on
   * loading / empty, so callers never have to repeat that correlation.
   */
  disabled?: boolean
  /**
   * Option list to render when neither loading nor empty. The children
   * are placed inside a `<Switch>` branch, so they only mount in the
   * loaded state and are torn down on the way back to loading or empty.
   */
  children: JSX.Element
}

/**
 * Stable `<select>` across the loading -> loaded transition. Swapping
 * the entire `<select>` with `<Show fallback>` would unmount the field
 * and reset selectedIndex; this component keeps the `<select>` instance
 * mounted and only swaps its option children, so the browser preserves
 * the field's value/focus across state transitions.
 *
 * Also normalizes the disable rule across every "loading select" in
 * the app: disabled when the caller asks, when loading, or when the
 * list has nothing to pick.
 */
export const LoadingSelect: Component<LoadingSelectProps> = (props) => {
  let selectEl: HTMLSelectElement | undefined
  // Re-apply props.value to the DOM whenever the Switch swaps option
  // children. The JSX binding `value={props.value}` only fires when
  // props.value itself changes; when the loading→loaded transition
  // swaps the children but value is unchanged (typical: caller seeded
  // gitMode.intent='feature' before mount, then waited for the inspect
  // RPC), the browser's selectedIndex stays on the first loaded option
  // ("main", "Select a branch...") and disagrees with form state. The
  // effect tracks both props.value AND props.loading + props.isEmpty so
  // it fires on every Switch transition, and re-applies value
  // unconditionally — the browser is the source of truth for what's
  // selected, so a redundant write on every transition is cheap.
  createEffect(() => {
    // Tracked reads — order matters: read all three so the effect
    // depends on them all even when value is the stable input.
    const v = props.value
    const loading = props.loading
    const isEmpty = props.isEmpty
    if (loading || isEmpty)
      return
    if (selectEl && selectEl.value !== v)
      selectEl.value = v
  })
  return (
    <select
      ref={selectEl}
      value={props.value}
      onChange={e => props.onChange(e.currentTarget.value)}
      disabled={(props.disabled ?? false) || props.loading || props.isEmpty}
    >
      <Switch>
        <Match when={props.loading}>
          <option value="">{props.loadingLabel}</option>
        </Match>
        <Match when={props.isEmpty}>
          <option value="">{props.emptyLabel}</option>
        </Match>
        <Match when={!props.loading && !props.isEmpty}>
          {props.children}
        </Match>
      </Switch>
    </select>
  )
}
