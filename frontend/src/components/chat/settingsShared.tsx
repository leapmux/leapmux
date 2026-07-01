import type { LucideIcon } from 'lucide-solid'
import type { Accessor, JSX } from 'solid-js'
import type { SettingsItem } from './settingsGroups'
import Check from 'lucide-solid/icons/check'
import ChevronsDown from 'lucide-solid/icons/chevrons-down'
import ChevronsUp from 'lucide-solid/icons/chevrons-up'
import Dot from 'lucide-solid/icons/dot'
import Flame from 'lucide-solid/icons/flame'
import Rocket from 'lucide-solid/icons/rocket'
import Sparkles from 'lucide-solid/icons/sparkles'
import Zap from 'lucide-solid/icons/zap'
import { createEffect, createMemo, createSignal, createUniqueId, For, Index, Show, splitProps } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { Tooltip } from '~/components/common/Tooltip'
import * as styles from './ChatView.css'
import { EFFORT_AUTO, OPTION_GROUP_SEARCHABLE_THRESHOLD } from './settingsGroups'

/**
 * Icon-per-effort-level map used by {@link effortIcon}. Effort ids are global
 * (not per-provider), so one map covers every tier: `ultracode` (xhigh +
 * workflow orchestration) gets a rocket, `xhigh` a flame, and `max` keeps Zap.
 */
export const DEFAULT_EFFORT_ICONS: Readonly<Record<string, LucideIcon>> = {
  [EFFORT_AUTO]: Sparkles,
  ultracode: Rocket,
  xhigh: Flame,
  max: Zap,
  high: ChevronsUp,
  medium: Dot,
  low: ChevronsDown,
  minimal: ChevronsDown,
  off: ChevronsDown,
  none: ChevronsDown,
}

/**
 * Render the icon for a thinking/effort level, falling back to the default map
 * then to a neutral Dot.
 */
export function effortIcon(level: string): JSX.Element {
  const I = DEFAULT_EFFORT_ICONS[level] ?? Dot
  return <Icon icon={I} size="xs" />
}

/**
 * Shared props for the settings selectors (OptionGroupSelect / RadioGroup /
 * SearchableSelect). All three render the same labelled, optionally-read-only
 * fieldset (see {@link SettingsGroupFieldset}) and differ only in the body, so the
 * common prop set lives here once; OptionGroupSelect and RadioGroup additionally take a
 * radio `name`.
 */
interface SettingsSelectProps {
  label: string
  items: SettingsItem[]
  testIdPrefix: string
  current: string
  onChange: (value: string) => void
  fieldsetClass?: string
  /** When true, the group is read-only: inputs are disabled and clicks don't fire onChange. */
  disabled?: boolean
  /** Tooltip shown on the whole group explaining why it's read-only (implies disabled styling). */
  disabledReason?: string
}

/**
 * Shared chrome for a settings group: the labelled `<div role="group">` with optional
 * read-only (disabled) styling, wrapped in a Tooltip when a disabledReason explains why
 * it's read-only. RadioGroup and SearchableSelect render their distinct body (radios /
 * current value + listbox) as children, so the fieldset, its label, the
 * data-disabled/aria-disabled toggling, and the disabled-reason tooltip live here once
 * rather than byte-for-byte in each.
 */
function SettingsGroupFieldset(props: {
  label: string
  fieldsetClass?: string
  disabled?: boolean
  disabledReason?: string
  children: JSX.Element
}): JSX.Element {
  const labelId = createUniqueId()
  const group = (
    <div
      role="group"
      aria-labelledby={labelId}
      // data-disabled / aria-disabled are added only when truthy, so a writable
      // group's DOM (and snapshots) stay byte-for-byte unchanged.
      data-disabled={props.disabled ? '' : undefined}
      aria-disabled={props.disabled ? 'true' : undefined}
      class={[styles.settingsFieldset, props.fieldsetClass].filter(Boolean).join(' ')}
    >
      <div id={labelId} class={styles.settingsGroupLabel}>{props.label}</div>
      {props.children}
    </div>
  )
  return (
    <Show when={props.disabledReason} fallback={group}>
      <Tooltip text={props.disabledReason}>{group}</Tooltip>
    </Show>
  )
}

/**
 * Generic option selector: RadioGroup for small lists, SearchableSelect for
 * lists exceeding {@link OPTION_GROUP_SEARCHABLE_THRESHOLD}. Used for every
 * option group (model, effort, permission mode, provider extras) so any axis
 * with many values becomes filterable, not just model.
 */
export function OptionGroupSelect(props: SettingsSelectProps & { name: string }): JSX.Element {
  // Both branches forward the same prop set; only RadioGroup also takes `name`.
  // splitProps keeps `common` a reactive proxy (a plain object literal would snapshot
  // the values once and break reactivity), so adding/renaming a shared prop is a single
  // edit rather than two parallel ones that can silently diverge.
  const [radioOnly, common] = splitProps(props, ['name'])
  return (
    <Show
      when={props.items.length > OPTION_GROUP_SEARCHABLE_THRESHOLD}
      fallback={<RadioGroup {...common} name={radioOnly.name} />}
    >
      <SearchableSelect {...common} />
    </Show>
  )
}

export function RadioGroup(props: SettingsSelectProps & { name: string }): JSX.Element {
  return (
    <SettingsGroupFieldset
      label={props.label}
      fieldsetClass={props.fieldsetClass}
      disabled={props.disabled}
      disabledReason={props.disabledReason}
    >
      {/*
        Index (not For) keys the radios by position so the <label> DOM nodes are
        STABLE across re-renders -- only their reactive content (value/checked/
        label) updates. The worker re-broadcasts the catalog on every status push
        and an optimistic model switch swaps the option list, both of which hand
        this a fresh items array; with For, each push detaches and recreates every
        radio, which flickers and races a Playwright/user click mid-recreation.
        Index recomputes each radio's value/checked from its position, so the
        rendered selection is always correct even when the list changes -- e.g. the
        effort list grows/shrinks per model, or the model list gains an entry
        mid-list when ensureSettledModelListed surfaces the resolved account-default
        at its canonical rank. The only thing not preserved across such a mid-list
        change is DOM node identity at/after the insertion point, a rare settle-time
        event we accept over the per-push churn For would cause.
      */}
      <Index each={props.items}>
        {item => (
          <Tooltip text={item().tooltip}>
            <label
              role="menuitemradio"
              class={styles.settingsRadioItem}
              data-testid={`${props.testIdPrefix}-${item().value}`}
            >
              <input
                type="radio"
                name={props.name}
                value={item().value}
                checked={props.current === item().value}
                disabled={props.disabled}
                onChange={() => {
                  // Guard against a programmatic change event firing while disabled.
                  if (!props.disabled)
                    props.onChange(item().value)
                }}
              />
              {item().label}
            </label>
          </Tooltip>
        )}
      </Index>
    </SettingsGroupFieldset>
  )
}

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
 * Used by SearchableSelect (inline in settings panels) and CodeLanguagePopover.
 */
export function FilterableListbox(props: {
  items: FilterableItem[]
  current?: string
  placeholder?: string
  testIdPrefix?: string
  onSelect: (value: string) => void
  onEscape?: () => void
  /** Auto-focus the filter input on mount. */
  autoFocus?: boolean
  /**
   * Optional "fresh view" trigger. Whenever this accessor's value changes, the
   * highlighted row and scroll position reset to the top. An always-mounted listbox
   * (e.g. the code-language popover, kept mounted so it measures at its real size)
   * used to get this reset for free from remounting; pass the open signal here so a
   * reopen starts at the top instead of a stale row (which Enter would mis-select).
   */
  resetKey?: Accessor<unknown>
  /** CSS class overrides. */
  listboxClass?: string
  itemClass?: string
  itemHighlightedClass?: string
  itemSelectedClass?: string
  controlClass?: string
  inputClass?: string
} & (
  // Controlled filter text: provide BOTH `filter` and `setFilter`, or NEITHER. When
  // provided, the caller owns the filter so it can reset it across reuse (e.g. a popover
  // that stays mounted between opens); otherwise the listbox manages it internally. The
  // discriminated union makes "only one of the pair" a compile error -- passing just one
  // would split-brain the input (it reads the controlled accessor but writes the internal
  // signal, or vice versa, so typing does nothing).
  | { filter?: undefined, setFilter?: undefined }
  | { filter: Accessor<string>, setFilter: (value: string) => void }
)): JSX.Element {
  const [internalFilter, setInternalFilter] = createSignal('')
  const filter = () => props.filter ? props.filter() : internalFilter()
  const setFilter = (value: string) => (props.setFilter ?? setInternalFilter)(value)
  const [highlightedIndex, setHighlightedIndex] = createSignal(0)
  let listRef: HTMLDivElement | undefined

  const filtered = createMemo(() => {
    const f = filter().toLowerCase()
    if (!f)
      return props.items
    return props.items.filter(item =>
      item.label.toLowerCase().includes(f)
      || item.value.toLowerCase().includes(f),
    )
  })

  // Keep highlightedIndex in range when the list shrinks underneath it -- props.items is
  // re-emitted as a shorter catalog on an optimistic model switch (the model/effort lists swap),
  // and the filter input is only one of the inputs that resize filtered(). Clamp (don't reset to
  // 0) so a benign re-broadcast doesn't yank a user mid-keyboard-navigation; only an out-of-range
  // index is pulled back to the last row. Without this, ArrowDown computes from a stale large
  // index and Enter indexes past the end (guarded, but selects nothing).
  createEffect(() => {
    const len = filtered().length
    setHighlightedIndex(i => (i > len - 1 ? Math.max(len - 1, 0) : i))
  })

  // Reset the highlighted row + scroll to the top whenever `resetKey` changes. The
  // effect tracks only `resetKey` (the writes below create no self-dependency), so an
  // uncontrolled caller that omits it just gets a one-time reset on mount.
  createEffect(() => {
    props.resetKey?.()
    setHighlightedIndex(0)
    if (listRef)
      listRef.scrollTop = 0
  })

  const scrollHighlightedIntoView = () => {
    requestAnimationFrame(() => {
      const el = listRef?.querySelectorAll<HTMLElement>('[data-listbox-item]')[highlightedIndex()]
      el?.scrollIntoView({ block: 'nearest' })
    })
  }

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

  const listboxCls = () => props.listboxClass || styles.searchableSelectListbox
  const itemCls = () => props.itemClass || styles.searchableSelectItem
  const itemHighlightCls = () => props.itemHighlightedClass || styles.searchableSelectItemHighlighted
  const itemSelectedCls = () => props.itemSelectedClass || styles.searchableSelectItemSelected
  const controlCls = () => props.controlClass || styles.searchableSelectControl
  const inputCls = () => props.inputClass || styles.searchableSelectInput

  return (
    <>
      <div class={listboxCls()} ref={listRef}>
        <For each={filtered()}>
          {(item, index) => {
            const row = (
              <div
                // Keyboard-nav scrolling looks the row up by this marker (works whether
                // or not the row is wrapped in a Tooltip's display:contents span).
                data-listbox-item=""
                class={`${itemCls()}${index() === highlightedIndex() ? ` ${itemHighlightCls()}` : ''}${props.current != null && item.value === props.current ? ` ${itemSelectedCls()}` : ''}`}
                data-testid={props.testIdPrefix ? `${props.testIdPrefix}-${item.value}` : undefined}
                onClick={() => props.onSelect(item.value)}
                onMouseEnter={() => setHighlightedIndex(index())}
              >
                <span>{highlightMatch(item.label, filter())}</span>
                <Show when={item.secondary}>
                  <span class={styles.searchableSelectItemSecondary}>
                    {highlightMatch(item.secondary!, filter())}
                  </span>
                </Show>
                <Show when={!item.secondary && props.current != null && item.value === props.current}>
                  <Icon icon={Check} size="xs" />
                </Show>
              </div>
            )
            // Only pay the Tooltip's per-row listeners + effects when there's
            // actual tooltip text. A 235-language picker has none, so wrapping
            // every row would be 235 idle Tooltip instances -- the bulk of the
            // list's population cost.
            return item.tooltip ? <Tooltip text={item.tooltip}>{row}</Tooltip> : row
          }}
        </For>
      </div>
      <div class={controlCls()} onClick={e => e.stopPropagation()}>
        <input
          class={inputCls()}
          placeholder={props.placeholder || 'Filter...'}
          value={filter()}
          onInput={(e) => {
            setFilter(e.currentTarget.value)
            setHighlightedIndex(0)
          }}
          onKeyDown={handleKeyDown}
          data-testid={props.testIdPrefix ? `${props.testIdPrefix}-filter` : undefined}
          ref={props.autoFocus
            ? (el: HTMLInputElement) => {
                requestAnimationFrame(() => {
                  el.focus()
                  el.select()
                })
              }
            : undefined}
        />
      </div>
    </>
  )
}

/** Searchable select for large item lists (e.g. models). */
export function SearchableSelect(props: SettingsSelectProps): JSX.Element {
  const currentLabel = () => {
    const item = props.items.find(i => i.value === props.current)
    return item?.label || props.current
  }

  return (
    <SettingsGroupFieldset
      label={props.label}
      fieldsetClass={props.fieldsetClass}
      disabled={props.disabled}
      disabledReason={props.disabledReason}
    >
      <div class={styles.searchableSelectCurrent} data-testid={`${props.testIdPrefix}-current`}>
        {currentLabel()}
      </div>
      <Show when={!props.disabled}>
        <FilterableListbox
          items={props.items}
          current={props.current}
          testIdPrefix={props.testIdPrefix}
          onSelect={props.onChange}
        />
      </Show>
    </SettingsGroupFieldset>
  )
}
