import type { Accessor, JSX } from 'solid-js'
import Check from 'lucide-solid/icons/check'
import { createEffect, createMemo, createSignal, For, Show, untrack } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { Tooltip } from '~/components/common/Tooltip'
import * as styles from './FilterableListbox.css'

/** Highlight matching substring in text (case-insensitive). */
function highlightMatch(text: string, filter: string): JSX.Element {
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
