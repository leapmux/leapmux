import type { JSX } from 'solid-js'
import type { AvailableModel, AvailableOptionGroup } from '~/generated/leapmux/v1/agent_pb'
import Check from 'lucide-solid/icons/check'
import { createMemo, createSignal, For, Show } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { Tooltip } from '~/components/common/Tooltip'
import * as styles from './ChatView.css'

/** Option group key for the permission mode setting, shared across providers. */
export const PERMISSION_MODE_KEY = 'permissionMode' as const

/** Option group keys for Claude Code-specific settings. */
export const OUTPUT_STYLE_KEY = 'outputStyle' as const
export const FAST_MODE_KEY = 'fastMode' as const
export const ALWAYS_THINKING_KEY = 'alwaysThinkingEnabled' as const

/** Shared item type used by RadioGroup and settings helpers. */
export interface SettingsItem {
  label: string
  value: string
  tooltip?: string
}

/** Build model radio items from available models. */
export function modelItems(availableModels: AvailableModel[] | undefined): SettingsItem[] {
  if (availableModels && availableModels.length > 0)
    return availableModels.map(m => ({ label: m.displayName || m.id, value: m.id, tooltip: m.description || undefined }))
  return []
}

/** Resolve the default model ID from the available models list. */
export function defaultModelId(availableModels: AvailableModel[] | undefined): string {
  if (!availableModels || availableModels.length === 0)
    return ''
  return availableModels.find(m => m.isDefault)?.id || availableModels[0]?.id || ''
}

/** Build effort radio items for the current model. */
export function effortItems(availableModels: AvailableModel[] | undefined, currentModel: string): SettingsItem[] {
  if (availableModels && availableModels.length > 0) {
    const model = availableModels.find(m => m.id === currentModel)
    if (model)
      return model.supportedEfforts.map(e => ({ label: e.name || e.id, value: e.id, tooltip: e.description || undefined }))
  }
  return []
}

/** Find an option group by key. */
export function optionGroup(availableOptionGroups: AvailableOptionGroup[] | undefined, key: string) {
  return availableOptionGroups?.find(g => g.key === key)
}

/** Resolve the display label for an option group key. */
export function optionGroupLabel(availableOptionGroups: AvailableOptionGroup[] | undefined, key: string): string {
  const group = optionGroup(availableOptionGroups, key)
  return group?.label || key
}

/** Build option-group radio items. */
export function optionGroupItems(availableOptionGroups: AvailableOptionGroup[] | undefined, key: string): SettingsItem[] {
  const group = optionGroup(availableOptionGroups, key)
  if (group && group.options.length > 0)
    return group.options.map(o => ({ label: o.name || o.id, value: o.id, tooltip: o.description || undefined }))
  return []
}

/** Find the permission mode option group. */
export function permissionModeGroup(availableOptionGroups: AvailableOptionGroup[] | undefined) {
  return optionGroup(availableOptionGroups, PERMISSION_MODE_KEY)
}

/** Build permission mode radio items. */
export function permissionModeItems(availableOptionGroups: AvailableOptionGroup[] | undefined): SettingsItem[] {
  return optionGroupItems(availableOptionGroups, PERMISSION_MODE_KEY)
}

/** Resolve model display name from available models. */
export function modelDisplayName(availableModels: AvailableModel[] | undefined, currentModel: string): string {
  if (availableModels && availableModels.length > 0) {
    const model = availableModels.find(m => m.id === currentModel)
    if (model)
      return model.displayName || model.id
  }
  return currentModel
}

/** Check if the current model has efforts. */
export function hasEfforts(availableModels: AvailableModel[] | undefined, currentModel: string): boolean {
  if (availableModels && availableModels.length > 0) {
    const model = availableModels.find(m => m.id === currentModel)
    return model ? model.supportedEfforts.length > 0 : false
  }
  return false
}

/** Resolve any option-group label from available option groups. */
export function optionLabel(availableOptionGroups: AvailableOptionGroup[] | undefined, key: string, currentValue: string): string {
  const group = optionGroup(availableOptionGroups, key)
  if (group) {
    const opt = group.options.find(o => o.id === currentValue)
    if (opt)
      return opt.name || opt.id
  }
  return currentValue
}

/** Threshold above which model selection uses a searchable select instead of radio buttons. */
const MODEL_SEARCHABLE_THRESHOLD = 7

/**
 * Model selector that uses RadioGroup for small lists and SearchableSelect
 * for lists exceeding the threshold.
 */
export function ModelSelect(props: {
  items: SettingsItem[]
  testIdPrefix: string
  name: string
  current: string
  onChange: (value: string) => void
  fieldsetClass?: string
}): JSX.Element {
  return (
    <Show
      when={props.items.length > MODEL_SEARCHABLE_THRESHOLD}
      fallback={(
        <RadioGroup
          label="Model"
          items={props.items}
          testIdPrefix={props.testIdPrefix}
          name={props.name}
          current={props.current}
          onChange={props.onChange}
          fieldsetClass={props.fieldsetClass}
        />
      )}
    >
      <SearchableSelect
        label="Model"
        items={props.items}
        testIdPrefix={props.testIdPrefix}
        current={props.current}
        onChange={props.onChange}
        fieldsetClass={props.fieldsetClass}
      />
    </Show>
  )
}

/** Resolve the default option ID for an option group. */
export function optionGroupDefaultValue(availableOptionGroups: AvailableOptionGroup[] | undefined, key: string): string {
  const group = optionGroup(availableOptionGroups, key)
  if (!group || group.options.length === 0)
    return ''
  return group.options.find(o => o.isDefault)?.id || group.options[0]?.id || ''
}

export function RadioGroup(props: {
  label: string
  items: { label: string, value: string, tooltip?: string }[]
  testIdPrefix: string
  name: string
  current: string
  onChange: (value: string) => void
  fieldsetClass?: string
}): JSX.Element {
  return (
    <fieldset class={[styles.settingsFieldset, props.fieldsetClass].filter(Boolean).join(' ')}>
      <legend class={styles.settingsGroupLabel}>{props.label}</legend>
      <For each={props.items}>
        {item => (
          <Tooltip text={item.tooltip}>
            <label
              role="menuitemradio"
              class={styles.settingsRadioItem}
              data-testid={`${props.testIdPrefix}-${item.value}`}
            >
              <input
                type="radio"
                name={props.name}
                value={item.value}
                checked={props.current === item.value}
                onChange={() => props.onChange(item.value)}
              />
              {item.label}
            </label>
          </Tooltip>
        )}
      </For>
    </fieldset>
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
  /** CSS class overrides. */
  listboxClass?: string
  itemClass?: string
  itemHighlightedClass?: string
  itemSelectedClass?: string
  controlClass?: string
  inputClass?: string
}): JSX.Element {
  const [filter, setFilter] = createSignal('')
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

  const handleKeyDown = (e: KeyboardEvent) => {
    const items = filtered()
    switch (e.key) {
      case 'ArrowDown':
        e.preventDefault()
        setHighlightedIndex(i => Math.min(i + 1, items.length - 1))
        requestAnimationFrame(() => {
          const el = listRef?.children[highlightedIndex()] as HTMLElement | undefined
          el?.scrollIntoView({ block: 'nearest' })
        })
        break
      case 'ArrowUp':
        e.preventDefault()
        setHighlightedIndex(i => Math.max(i - 1, 0))
        requestAnimationFrame(() => {
          const el = listRef?.children[highlightedIndex()] as HTMLElement | undefined
          el?.scrollIntoView({ block: 'nearest' })
        })
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
          {(item, index) => (
            <div
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
          )}
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
export function SearchableSelect(props: {
  label: string
  items: SettingsItem[]
  testIdPrefix: string
  current: string
  onChange: (value: string) => void
  fieldsetClass?: string
}): JSX.Element {
  const currentLabel = () => {
    const item = props.items.find(i => i.value === props.current)
    return item?.label || props.current
  }

  return (
    <fieldset class={[styles.settingsFieldset, props.fieldsetClass].filter(Boolean).join(' ')}>
      <legend class={styles.settingsGroupLabel}>{props.label}</legend>
      <div class={styles.searchableSelectCurrent} data-testid={`${props.testIdPrefix}-current`}>
        {currentLabel()}
      </div>
      <FilterableListbox
        items={props.items}
        current={props.current}
        testIdPrefix={props.testIdPrefix}
        onSelect={props.onChange}
      />
    </fieldset>
  )
}
