import type { JSXElement } from 'solid-js'
import { createMemo, For, onCleanup } from 'solid-js'
import { nextFilterTab } from './FilterTabBar'
import * as styles from './PillGroup.css'

/**
 * One pill, as a member of a one-of-N group (role=radio).
 *
 * Selection used to be a CSS class alone, twelve times over, so a screen reader
 * read each group as identical stateless buttons. `aria-pressed` fixed the
 * "stateless" half and still described the wrong widget: these groups are all
 * one-of-N, and a toggle button announces "pressed" with no group name and no
 * set position, while promising it can be un-pressed (clicking the selected
 * pill does nothing). role=radio inside a named role=radiogroup announces
 * "Theme, Dark, radio button, checked, 2 of 3".
 *
 * Not exported: a pill outside a PillGroup has no radiogroup to belong to, and
 * a lone role=radio is worse than a plain button. A genuine two-state control
 * is a different widget — a button with `aria-pressed`, which announces that
 * un-pressing is available. Do not reach for this one there.
 */
function PillOption(props: {
  selected: boolean
  /** Whether this pill is the group's single tab stop. See PillGroup. */
  roving: boolean
  onClick: () => void
  ref?: (el: HTMLButtonElement) => void
  children: JSXElement
}) {
  return (
    <button
      class={props.selected ? styles.pillOptionActive : styles.pillOption}
      role="radio"
      aria-checked={props.selected}
      // Roving: Tab reaches the GROUP, arrows move within it (see PillGroup).
      // Keyed on `roving`, NOT on `selected`: a group whose stored value
      // matches no option has nothing selected, and anchoring the tab stop
      // on the selection made every pill -1 there — Tab skipped the whole
      // radiogroup and the arrow keys, which the group handles, reached
      // nothing. `aria-checked` stays on `selected`, because the APG
      // radiogroup rule allows an unchecked radio to hold the tab stop.
      tabIndex={props.roving ? 0 : -1}
      ref={el => props.ref?.(el)}
      onClick={() => props.onClick()}
    >
      {props.children}
    </button>
  )
}

/**
 * A one-of-N pill group: the radiogroup wrapper, its accessible name, and the
 * arrow-key contract the role requires.
 *
 * `label` is what assistive tech announces on entry and is the piece a row of
 * bare buttons never had -- the visible <h3> above each group is not associated
 * with it in the accessibility tree. The arrow/Home/End math adopts the pure
 * `nextFilterTab` from FilterTabBar rather than duplicating it, so the tab bar
 * and the pill groups keep one roving-tabindex implementation.
 */
export function PillGroup<T>(props: {
  label: string
  options: { value: T, label: JSXElement }[]
  selected: (value: T) => boolean
  onSelect: (value: T) => void
}) {
  // Keyed by option value, not by index: `<For>` reuses a row it MOVES
  // without re-invoking its ref, so an index-keyed array points at the
  // wrong button after a reorder and `selectAt` moves focus to a pill the
  // user did not pick. FilterTabBar keys its tabs the same way, and for the
  // same reason.
  const els = new Map<T, HTMLButtonElement>()

  /**
   * The option that holds the group's single tab stop.
   *
   * The selected one, or the FIRST when the group has no selection — a
   * browser preference read back from storage can hold a value that matches
   * no option, and a group that no key can reach is a group the user cannot
   * change.
   */
  const rovingIndex = createMemo(() => {
    const i = props.options.findIndex(o => props.selected(o.value))
    return i < 0 ? 0 : i
  })

  const selectAt = (i: number) => {
    const opt = props.options[i]
    if (!opt)
      return
    props.onSelect(opt.value)
    els.get(opt.value)?.focus()
  }
  const onKeyDown = (e: KeyboardEvent) => {
    const values = props.options.map(o => o.value)
    // The arrow origin is the pill that CARRIES the tab stop, which is
    // where focus sits. Deriving it from the selection instead passed
    // undefined for a group with none, and every arrow key then moved
    // relative to the first option whatever was focused.
    const next = nextFilterTab(values, values[rovingIndex()], e.key)
    if (next === undefined)
      return
    e.preventDefault()
    selectAt(values.indexOf(next))
  }
  return (
    <div class={styles.pillGroup} role="radiogroup" aria-label={props.label} onKeyDown={onKeyDown}>
      <For each={props.options}>
        {(opt, i) => (
          <PillOption
            selected={props.selected(opt.value)}
            roving={i() === rovingIndex()}
            onClick={() => props.onSelect(opt.value)}
            ref={(el) => {
              els.set(opt.value, el)
              // Solid does not re-invoke a ref with null on disposal, so a
              // shrinking option list would leave a detached button in the
              // map. The identity check keeps a remove-then-re-add of the
              // same value from deleting the NEW element's entry.
              onCleanup(() => {
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
  )
}
