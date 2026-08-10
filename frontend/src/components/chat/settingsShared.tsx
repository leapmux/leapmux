import type { Accessor, JSX } from 'solid-js'
import type { SettingsItem } from './settingsGroups'
import Check from 'lucide-solid/icons/check'
import { createEffect, createMemo, createSignal, For, Index, Show, untrack } from 'solid-js'
import { DropdownMenuCheckableItem } from '~/components/common/DropdownMenu'
import { Icon } from '~/components/common/Icon'
import { Tooltip } from '~/components/common/Tooltip'
import { OPTION_GROUP_SEARCHABLE_THRESHOLD } from './settingsGroups'
import * as styles from './settingsShared.css'

/** Highlight matching substring in text (case-insensitive). */
export function highlightMatch(text: string, filter: string): JSX.Element {
  if (!filter)
    return <>{text}</>
  const idx = text.toLowerCase().indexOf(filter.toLowerCase())
  if (idx < 0)
    return <>{text}</>
  return (
    <>
      {text.slice(0, idx)}
      <strong>{text.slice(idx, idx + filter.length)}</strong>
      {text.slice(idx + filter.length)}
    </>
  )
}

/** Item type for FilterableListbox. */
export interface FilterableItem {
  /** Display label. */
  label: string
  /** Unique value/id. */
  value: string
  /** Optional secondary text shown right-aligned. */
  secondary?: string
  /** Optional hover tooltip (e.g. an option's description). */
  tooltip?: string
}

/**
 * Reusable filterable listbox with keyboard navigation and search input.
 * Used by OptionGroupMenuItems (for large option groups) and CodeLanguagePopover.
 */
export function FilterableListbox(props: {
  items: FilterableItem[]
  current?: string
  placeholder?: string
  testIdPrefix?: string
  onSelect: (value: string) => void
  onEscape?: () => void
  /**
   * Take focus when the view becomes fresh (see `resetKey`), and on mount when
   * no `resetKey` is supplied.
   */
  autoFocus?: boolean
  /**
   * Optional "fresh view" trigger. Whenever this accessor's value changes, the
   * filter text, the highlighted row, and the scroll position all reset, and
   * `autoFocus` takes focus.
   *
   * A popover keeps its children mounted across a close, so a caller that hosts
   * this listbox in one MUST pass its open accessor here. Without it the filter
   * the user typed last time is still applied on the next open, and the focus a
   * mount-time ref took landed on a `display: none` input and did nothing.
   */
  resetKey?: Accessor<unknown>
} & (
  // Controlled filter text: provide BOTH `filter` and `setFilter`, or NEITHER.
  | { filter?: undefined, setFilter?: undefined }
  | { filter: Accessor<string>, setFilter: (value: string) => void }
)): JSX.Element {
  const [internalFilter, setInternalFilter] = createSignal('')
  const filter = () => props.filter ? props.filter() : internalFilter()
  const setFilter = (value: string) => (props.setFilter ?? setInternalFilter)(value)
  const [highlightedIndex, setHighlightedIndex] = createSignal(0)
  let listRef: HTMLDivElement | undefined
  let inputRef: HTMLInputElement | undefined

  const filtered = createMemo(() => {
    const f = filter().toLowerCase()
    if (!f)
      return props.items
    return props.items.filter(item =>
      item.label.toLowerCase().includes(f)
      || item.value.toLowerCase().includes(f),
    )
  })

  const scrollHighlightedIntoView = () => {
    requestAnimationFrame(() => {
      const el = listRef?.querySelectorAll<HTMLElement>('[data-listbox-item]')[highlightedIndex()]
      el?.scrollIntoView({ block: 'nearest' })
    })
  }

  createEffect(() => {
    const len = filtered().length
    setHighlightedIndex(i => (i > len - 1 ? Math.max(len - 1, 0) : i))
  })

  // A fresh view: clear the filter, put the highlight on the selected row, show
  // that row, and take focus. Everything below `resetKey` is untracked, so the
  // effect fires on `resetKey` ALONE. Tracking `items` or `current` here would
  // re-snap the highlight on every worker catalog re-broadcast and yank a user
  // out of a keyboard selection -- the clamp effect above is what handles a list
  // that shrinks underneath the highlight.
  createEffect(() => {
    props.resetKey?.()
    untrack(() => {
      // Only the internal filter: a controlled caller owns its own text.
      if (!props.filter)
        setInternalFilter('')
      // Start on the selected row, not the top. This list is the only place a
      // large group shows which value is active, and the marker is a check icon
      // on that row -- so opening at the top hides the answer whenever the
      // selection is below the fold. It also makes Enter on a fresh open
      // re-select the current value instead of silently switching to the first
      // option.
      const rows = filtered()
      const selected = props.current == null ? -1 : rows.findIndex(i => i.value === props.current)
      setHighlightedIndex(selected < 0 ? 0 : selected)
      if (listRef)
        listRef.scrollTop = 0
      if (selected > 0)
        scrollHighlightedIntoView()
      // Focus HERE rather than from a mount-time ref on the input. A popover
      // renders its children before it opens and the UA hides a closed popover
      // with `display: none`, so a mount-time focus() is a silent no-op and
      // never runs again. `resetKey` is the caller's "this view is fresh"
      // signal, which is exactly when focus is meaningful.
      if (props.autoFocus) {
        requestAnimationFrame(() => {
          inputRef?.focus()
          inputRef?.select()
        })
      }
    })
  })

  const handleKeyDown = (e: KeyboardEvent) => {
    const items = filtered()
    switch (e.key) {
      case 'ArrowDown':
        e.preventDefault()
        setHighlightedIndex(i => Math.min(i + 1, items.length - 1))
        scrollHighlightedIntoView()
        break
      case 'ArrowUp':
        e.preventDefault()
        setHighlightedIndex(i => Math.max(i - 1, 0))
        scrollHighlightedIntoView()
        break
      case 'Enter': {
        e.preventDefault()
        const item = items[highlightedIndex()]
        if (item)
          props.onSelect(item.value)
        break
      }
      case 'Escape':
        if (props.onEscape)
          props.onEscape()
        break
    }
  }

  return (
    <>
      <div class={styles.comboboxListbox} ref={listRef}>
        <For each={filtered()}>
          {(item, index) => {
            const selected = () => props.current != null && item.value === props.current
            const row = (
              <div
                data-listbox-item=""
                class={[styles.comboboxItem, index() === highlightedIndex() ? styles.comboboxItemHighlighted : '', selected() ? styles.comboboxItemSelected : ''].filter(Boolean).join(' ')}
                data-testid={props.testIdPrefix ? `${props.testIdPrefix}-${item.value}` : undefined}
                onClick={() => props.onSelect(item.value)}
                onMouseEnter={() => setHighlightedIndex(index())}
              >
                <span>{highlightMatch(item.label, filter())}</span>
                <Show when={item.secondary}>
                  <span class={styles.comboboxItemSecondary}>{highlightMatch(item.secondary!, filter())}</span>
                </Show>
                {/* The check icon and the secondary text share the trailing
                    slot, so a row with secondary text is marked selected by
                    weight alone (see the selected style). */}
                <Show when={!item.secondary && selected()}>
                  <Icon icon={Check} size="xs" />
                </Show>
              </div>
            )
            return item.tooltip ? <Tooltip text={item.tooltip}>{row}</Tooltip> : row
          }}
        </For>
      </div>
      <div class={styles.comboboxControl} onClick={e => e.stopPropagation()}>
        <input
          class={styles.comboboxInput}
          placeholder={props.placeholder || 'Filter...'}
          value={filter()}
          onInput={(e) => {
            setFilter(e.currentTarget.value)
            setHighlightedIndex(0)
          }}
          onKeyDown={handleKeyDown}
          data-testid={props.testIdPrefix ? `${props.testIdPrefix}-filter` : undefined}
          ref={(el: HTMLInputElement) => (inputRef = el)}
        />
      </div>
    </>
  )
}

/**
 * Props for {@link OptionGroupMenuItems}.
 */
export interface OptionGroupMenuItemsProps {
  /** Display label for the group (used only for tooltips / fallbacks). */
  label: string
  /** The group's options as {@link SettingsItem}s. */
  items: SettingsItem[]
  /** Test-id prefix for each option item. */
  testIdPrefix: string
  /** Currently selected value. */
  current: string
  /** Called when the user selects a value. */
  onChange: (value: string) => void
  /** When true, items are disabled and clicks don't fire onChange. */
  disabled?: boolean
  /** Tooltip explaining why the group is read-only. */
  disabledReason?: string
  /**
   * The host popover's open accessor. A popover keeps its children mounted
   * across a close, so the filterable branch needs this to reset its filter and
   * take focus on each open — see {@link FilterableListbox}'s `resetKey`.
   */
  openKey?: Accessor<unknown>
}

/**
 * Menu items for a single option group, designed to be placed directly inside a
 * `DropdownMenu` (submenu or chip popover). Two modes:
 *
 * - **≤ 7 options**: each option is a `<button role="menuitemradio">` with a
 *   disabled OAT radio showing the selected state.
 * - **> 7 options**: a `FilterableListbox` (the same filter control used by the
 *   code-language popover) with keyboard navigation.
 */
export function OptionGroupMenuItems(props: OptionGroupMenuItemsProps): JSX.Element {
  const useFilterable = () => props.items.length > OPTION_GROUP_SEARCHABLE_THRESHOLD

  return (
    <Show
      when={useFilterable()}
      fallback={(
        <Index each={props.items}>
          {(item) => {
            const row = () => (
              <DropdownMenuCheckableItem
                kind="radio"
                label={item().label}
                checked={props.current === item().value}
                disabled={props.disabled}
                title={props.disabled ? props.disabledReason : undefined}
                data-testid={`${props.testIdPrefix}-${item().value}`}
                onSelect={() => props.onChange(item().value)}
              />
            )
            // Wrap only when there is tooltip text. A Tooltip mounts its own
            // wrapper and listeners even with nothing to show, and most option
            // values carry no description.
            return (
              <Show when={item().tooltip} fallback={row()}>
                {tip => <Tooltip text={tip()}>{row()}</Tooltip>}
              </Show>
            )
          }}
        </Index>
      )}
    >
      <Show
        when={!props.disabled}
        fallback={(
          // Read-only group: the list is not offered, so show the current
          // selection. The label alone would leave the user with no way to see
          // which value the agent is actually running.
          <span title={props.disabledReason}>
            {props.items.find(i => i.value === props.current)?.label || props.label}
          </span>
        )}
      >
        <FilterableListbox
          items={props.items}
          current={props.current}
          testIdPrefix={props.testIdPrefix}
          onSelect={props.onChange}
          autoFocus
          resetKey={props.openKey}
        />
      </Show>
    </Show>
  )
}
