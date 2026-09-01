import type { Component } from 'solid-js'
import ChevronDown from 'lucide-solid/icons/chevron-down'
import { createMemo, createSignal, For, Match, Show, Switch } from 'solid-js'
import { DropdownMenu, DropdownMenuCheckableItem } from '~/components/common/DropdownMenu'
import { Icon } from '~/components/common/Icon'
import * as styles from './LoadingMenu.css'

/** One choice. `group` puts it under a heading; entries keep their given order. */
export interface LoadingMenuOption {
  value: string
  label: string
  group?: string
}

export interface LoadingMenuProps {
  'value': string
  'onChange': (value: string) => void
  /**
   * The option list is still loading, and this is what the trigger and the menu
   * body show while it is. Omit it when the caller holds its list already.
   *
   * ONE prop, not a `loading` flag beside a `loadingLabel`. The pair let a
   * caller arm the flag with no label, which renders an empty trigger, and let
   * two callers pass a label that can never render because their flag is a
   * literal `false`. Presence is the state, so neither mistake can be written.
   */
  'loadingLabel'?: string
  'emptyLabel': string
  /**
   * Caller-controlled disabled flag. OR'd with the internal disable on
   * loading / empty, so callers never have to repeat that correlation.
   */
  'disabled'?: boolean
  /** Accessible name for the trigger and the menu. */
  'ariaLabel': string
  /** Shown on the trigger when `value` matches no option — the "pick one" prompt. */
  'placeholder'?: string
  'options': LoadingMenuOption[]
  /**
   * Offer a filter box above the list.
   *
   * DERIVED from the option count when the caller says nothing: a list of
   * FILTER_MIN_OPTIONS or more gets one. Scanning a long menu by eye is the
   * work the filter removes, and which menus are long is a property of the
   * DATA, not something each call site should have to predict -- `BranchSelect`
   * knew its list was unbounded, but a machine with twenty shells produces a
   * long `ShellSelector` that nobody declared.
   *
   * Pass it explicitly to override in either direction. A short list gets no
   * filter, because a filter over five entries is noise.
   */
  'filter'?: boolean
  /**
   * Called when the menu opens.
   *
   * The seam the native `<select>` had as `onFocus` + `onPointerDown`:
   * `WorkerSelector` warms the metadata for the workers it is ABOUT TO LIST.
   *
   * Deliberately tied to the open, not to the worker list arriving. The
   * prefetch skips the selected worker and fans out an RPC per remaining
   * online worker, so firing it whenever the list changes turns a lazy warm-up
   * into a storm on every refresh -- and the trigger's own label never needed
   * it, because the selected worker's metadata arrives by another path.
   */
  'onOpen'?: () => void
  'data-testid'?: string
}

/**
 * The option count at which a menu grows a filter box.
 *
 * Above a dozen, finding one entry by eye is real work and the filter is the
 * cheapest way to cut it. Below, the box costs a row of chrome and a focus stop
 * for a list the user can read at a glance.
 *
 * `EnumControl` switches from pills to a menu at 4; this is the second
 * threshold on the same axis, and the two are independent on purpose -- one is
 * about how much horizontal room a row of pills needs, this is about how long a
 * list a reader can scan.
 */
const FILTER_MIN_OPTIONS = 12

/**
 * A one-of-N menu that survives the loading -> loaded transition.
 *
 * Replaces the native `<select>` this used to wrap; see the dropdown rule in
 * CLAUDE.md. The swap deleted a whole class of bug with it: a `<select>` keeps
 * its selection in `selectedIndex`, which is BROWSER state, so the old component
 * needed an effect that re-applied `value` every time the option children were
 * swapped -- otherwise a list that arrived after the caller had already seeded
 * `value` left the field showing the first option and disagreeing with form
 * state. A menu derives its checked item from `value` on every render, so there
 * is nothing to resynchronise and no effect to get wrong.
 *
 * It still normalizes the disable rule across every "loading select" in the app:
 * disabled when the caller asks, when loading, or when the list has nothing to
 * pick.
 */
export const LoadingMenu: Component<LoadingMenuProps> = (props) => {
  const [query, setQuery] = createSignal('')
  let popoverEl: HTMLElement | undefined
  let filterEl: HTMLInputElement | undefined

  const testId = () => props['data-testid'] ?? 'loading-menu'

  /** Loading is exactly "a loading label was given". */
  const loading = () => props.loadingLabel !== undefined

  /**
   * Whether this menu shows a filter box.
   *
   * The caller's word when it gave one; otherwise the option count decides.
   * Derived rather than declared, because how long a list is depends on the
   * user's data -- a repository's branches, a machine's shells -- and not on
   * anything the call site knows when it is written.
   */
  const filtering = () => props.filter ?? props.options.length >= FILTER_MIN_OPTIONS

  /**
   * Whether there is nothing to choose from.
   *
   * DERIVED, not declared. It was a required prop, and every caller answered it
   * with the length of the very list it passed as `options` -- so the prop was
   * a second copy of a fact `options` already carries, and the two could
   * disagree. `BranchSelect` made them disagree: it injected its own "Select a
   * branch..." row into `options`, so in a repository with no branches the list
   * was one entry long while `isEmpty` said true, and the menu disabled itself
   * around a row it had just rendered. That row is what `placeholder` is for,
   * and BranchSelect now passes one.
   *
   * It reads the FULL list rather than `visible()`: a filter that matches
   * nothing leaves plenty to choose from, and disabling the trigger under the
   * user's own query would trap them with no way to clear it.
   */
  const isEmpty = () => props.options.length === 0

  const disabled = () => (props.disabled ?? false) || loading() || isEmpty()

  const visible = createMemo(() => {
    const q = query().trim().toLowerCase()
    if (!filtering() || q === '')
      return props.options
    return props.options.filter(o => o.label.toLowerCase().includes(q))
  })

  /**
   * The label on the trigger. Four states, not three.
   *
   * A value that matches NO option is its own case, and it is not the empty
   * list: falling through to `emptyLabel` made the trigger read "No workers
   * online" or "No options" above a populated menu, which states the opposite
   * of what the user can see. The raw value is the truthful answer and it is
   * what the reader needs in order to repair the setting, so an unmatched
   * non-empty value shows itself.
   *
   * `placeholder` stays for the genuinely EMPTY value -- the "pick one" prompt
   * that `BranchSelect` and `WorktreeSelect` render before a choice is made.
   */
  const triggerText = () => {
    const loadingLabel = props.loadingLabel
    if (loadingLabel !== undefined)
      return loadingLabel
    if (isEmpty())
      return props.emptyLabel
    const match = props.options.find(o => o.value === props.value)
    if (match)
      return match.label
    if (props.value === '')
      return props.placeholder ?? props.emptyLabel
    return props.value
  }

  const select = (value: string) => {
    props.onChange(value)
    // A filtered menu renders `as="div"` so a click in the filter box does not
    // dismiss it, which also means an item click does not either. Closing here
    // is what that trade costs, and only the item knows the action ran.
    if (filtering()) {
      popoverEl?.hidePopover()
      setQuery('')
    }
  }

  /**
   * The list, and the two states that must REPLACE it rather than sit beside it.
   *
   * `loading` tears the options down. The predecessor restricted its children the
   * same way, and dropping that gate left a stale list mounted and clickable:
   * a fetcher keeps the previous entries until its next success, `DropdownMenu`
   * renders children eagerly, and the item rows take no `disabled` -- so a
   * refresh fired while the popover is open (F5 and $mod+r carry no `when`
   * clause) left the user picking a branch from the repository that was
   * replaced. Disabling the TRIGGER cannot help once the popover is already
   * open.
   */
  const items = () => (
    <Switch>
      <Match when={props.loadingLabel !== undefined}>
        <div class={styles.emptyNote}>{props.loadingLabel}</div>
      </Match>
      <Match when={isEmpty()}>
        <div class={styles.emptyNote}>{props.emptyLabel}</div>
      </Match>
      <Match when={visible().length === 0}>
        <div class={styles.emptyNote}>No matches</div>
      </Match>
      <Match when={visible().length > 0}>
        <For each={visible()}>
          {(option, i) => (
            <>
              {/* A heading whenever the group changes, so Local and Remote read
                  apart the way `<optgroup>` used to render them. */}
              <Show when={option.group && option.group !== visible()[i() - 1]?.group}>
                <div class={styles.groupHeading}>{option.group}</div>
              </Show>
              <DropdownMenuCheckableItem
                kind="radio"
                label={option.label}
                checked={option.value === props.value}
                data-testid={`loading-menu-option-${option.value}`}
                onSelect={() => select(option.value)}
              />
            </>
          )}
        </For>
      </Match>
    </Switch>
  )

  return (
    <DropdownMenu
      aria-label={props.ariaLabel}
      as={filtering() ? 'div' : 'menu'}
      popoverRef={el => (popoverEl = el)}
      onToggle={(open) => {
        if (open) {
          props.onOpen?.()
          // Put the caret in the filter box. `popover="auto"` moves focus
          // nowhere on its own, so without this the filter buys back none of
          // the type-ahead a `<select>` gave: the user opens the menu, types,
          // and nothing happens until they click into the box. Deferred to the
          // next frame because the popover is not yet visible when `toggle`
          // fires, and `focus()` on a hidden element does nothing.
          requestAnimationFrame(() => filterEl?.focus())
        }
        else {
          setQuery('')
        }
      }}
      data-testid={testId()}
      trigger={triggerProps => (
        <button
          {...triggerProps}
          type="button"
          class={styles.trigger}
          aria-label={props.ariaLabel}
          // Derived from the menu's own id, because `DropdownMenu` puts that id
          // on the POPOVER and renders the trigger as its sibling -- so a
          // caller cannot reach the trigger by descending into the menu.
          data-testid={`${testId()}-trigger`}
          data-value={props.value}
          disabled={disabled()}
        >
          <span class={styles.triggerText}>{triggerText()}</span>
          <Icon icon={ChevronDown} size="xs" aria-hidden="true" />
        </button>
      )}
    >
      <Show when={filtering()}>
        <input
          type="text"
          class={styles.filterInput}
          placeholder="Filter…"
          aria-label={`Filter ${props.ariaLabel}`}
          data-testid="loading-menu-filter"
          ref={filterEl}
          value={query()}
          onInput={e => setQuery(e.currentTarget.value)}
        />
      </Show>
      {/* The filtered form renders `as="div"`, so the popover is a plain
          element with no menu semantics of its own -- and ARIA requires a
          `menuitemradio` to be owned by a `menu`, `menubar` or `group`.
          Without this wrapper a screen reader announced the branch picker (the
          one caller that filters, and the longest list in the app) as a set of
          orphaned radios with no group name and no set position.

          Wrapped HERE and not on the popover, because `as="div"` serves two
          different callers: this one, whose items ARE the popover's children,
          and a panel like `FilesSortMenu` that supplies its own inner
          `role="menu"` around several groups. A role on the popover would give
          that panel two nested menus. The unfiltered form needs none of this --
          `DropdownMenu` renders a real `<menu>` there. */}
      <Show when={filtering()} fallback={items()}>
        <div role="menu" aria-label={props.ariaLabel}>{items()}</div>
      </Show>
    </DropdownMenu>
  )
}
