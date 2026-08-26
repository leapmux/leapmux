import type { JSXElement } from 'solid-js'
import { createMemo, For, onCleanup, Show } from 'solid-js'
import { nextFilterTab } from './FilterTabBar'
import * as styles from './PillGroup.css'
import { Tooltip } from './Tooltip'

/** One choice of a {@link PillGroup}. */
export interface PillOptionSpec<T> {
  value: T
  label: JSXElement
  /**
   * Refuse THIS option, and say why.
   *
   * Shown-and-refused rather than removed, for an option the reader can get
   * back: an option that vanishes leaves somebody who needs it with no way to
   * learn that it exists or what would restore it. Remove an option instead
   * when the deployment simply does not have it -- there the reason is not the
   * reader's to act on, and a permanently dead pill is only noise.
   *
   * The reason reaches the reader through `<Tooltip>`, so it works on the
   * disabled pill and does not become the pill's accessible name.
   */
  disabledReason?: string
}

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
  /** Show the selection, refuse the click. See PillGroup. */
  disabled: boolean
  /** Why THIS pill is refused, when it is refused on its own account. */
  disabledReason?: string
  onClick: () => void
  ref?: (el: HTMLButtonElement) => void
  children: JSXElement
}) {
  const pill = () => (
    <button
      // A bare <button> inside a <form> defaults to type="submit", so every
      // pill click SUBMITTED the form it sat in. On the login page that
      // meant choosing "Passkey" started a sign-in with the username the
      // user had just typed and nothing else -- the button went to
      // "Signing in..." and stayed there. A pill selects; it never submits.
      type="button"
      class={props.selected ? styles.pillOptionActive : styles.pillOption}
      role="radio"
      aria-checked={props.selected}
      // The NATIVE attribute, which assistive tech maps to `aria-disabled` on
      // its own. It also removes the pill from the tab order, so the roving
      // index below cannot hand the group a tab stop it refuses to act on.
      disabled={props.disabled}
      // Roving: Tab reaches the GROUP, arrows move within it (see PillGroup).
      // Keyed on `roving`, NOT on `selected`: a group whose stored value
      // matches no option has nothing selected, and anchoring the tab stop
      // on the selection made every pill -1 there — Tab skipped the whole
      // radiogroup and the arrow keys, which the group handles, reached
      // nothing. `aria-checked` stays on `selected`, because the APG
      // radiogroup rule allows an unchecked radio to hold the tab stop.
      tabIndex={props.roving && !props.disabled ? 0 : -1}
      ref={el => props.ref?.(el)}
      onClick={() => props.onClick()}
    >
      {props.children}
    </button>
  )
  // Wrapped only when there is something to say. A Tooltip mounts its own
  // wrapper, listeners and attribute observer even with nothing to show, and
  // most pills carry no reason at all.
  return (
    <Show when={props.disabledReason} fallback={pill()}>
      {reason => <Tooltip text={reason()}>{pill()}</Tooltip>}
    </Show>
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
  options: PillOptionSpec<T>[]
  selected: (value: T) => boolean
  onSelect: (value: T) => void
  /**
   * Show the current selection and refuse to change it.
   *
   * For a group another control governs -- the theme chooser's mode pills while
   * "Match UI theme" is on. The pills stay VISIBLE rather than being removed,
   * because what they display is the answer the governing control produced, and
   * a user who cannot see it cannot tell what turning the switch off would give
   * them.
   */
  disabled?: boolean
}) {
  // Keyed by option value, not by index: `<For>` reuses a row it MOVES
  // without re-invoking its ref, so an index-keyed array points at the
  // wrong button after a reorder and `selectAt` moves focus to a pill the
  // user did not pick. FilterTabBar keys its tabs the same way, and for the
  // same reason.
  const els = new Map<T, HTMLButtonElement>()

  /** Refused, by the group's own flag or by the option's own reason. */
  const refused = (opt: PillOptionSpec<T>) =>
    props.disabled === true || opt.disabledReason !== undefined

  /** The values a key can move to. A refused pill is not one of them. */
  const reachable = createMemo(() => props.options.filter(o => !refused(o)).map(o => o.value))

  /**
   * The option that holds the group's single tab stop.
   *
   * The selected one, or the first REACHABLE one — a browser preference read
   * back from storage can hold a value that matches no option, and a group
   * that no key can reach is a group the user cannot change. A selection that
   * is itself refused hands the tab stop on for the same reason: the APG
   * radiogroup rule allows an unchecked radio to hold it, and a refused pill
   * takes no tab stop at all (see `tabIndex` on PillOption).
   *
   * With EVERY option refused there is nothing to hand it to, so it stays on
   * the selection (or the first pill) and `tabIndex` resolves it to -1.
   */
  const rovingIndex = createMemo(() => {
    const selected = props.options.findIndex(o => props.selected(o.value))
    if (selected >= 0 && !refused(props.options[selected]!))
      return selected
    const firstReachable = props.options.findIndex(o => !refused(o))
    if (firstReachable >= 0)
      return firstReachable
    return selected < 0 ? 0 : selected
  })

  const selectAt = (value: T | undefined) => {
    const opt = props.options.find(o => o.value === value)
    if (!opt || refused(opt))
      return
    props.onSelect(opt.value)
    els.get(opt.value)?.focus()
  }
  const onKeyDown = (e: KeyboardEvent) => {
    const values = reachable()
    if (values.length === 0)
      return
    // The arrow origin is the pill that CARRIES the tab stop, which is
    // where focus sits. Deriving it from the selection instead passed
    // undefined for a group with none, and every arrow key then moved
    // relative to the first option whatever was focused.
    const origin = props.options[rovingIndex()]?.value
    const next = nextFilterTab(values, values.includes(origin!) ? origin : values[0], e.key)
    if (next === undefined)
      return
    e.preventDefault()
    selectAt(next)
  }
  return (
    <div class={styles.pillGroup} role="radiogroup" aria-label={props.label} onKeyDown={onKeyDown}>
      <For each={props.options}>
        {(opt, i) => (
          <PillOption
            selected={props.selected(opt.value)}
            roving={i() === rovingIndex()}
            disabled={refused(opt)}
            disabledReason={opt.disabledReason}
            onClick={() => selectAt(opt.value)}
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
