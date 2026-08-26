import type { Accessor, JSX } from 'solid-js'
import type { SettingsItem } from './settingsGroups'
import { Index, Show } from 'solid-js'
import { DropdownMenuCheckableItem } from '~/components/common/DropdownMenu'
import { FilterableListbox } from '~/components/common/FilterableListbox'
import { Tooltip } from '~/components/common/Tooltip'
import { OPTION_GROUP_SEARCHABLE_THRESHOLD } from './settingsGroups'

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
                data-testid={`${props.testIdPrefix}-${item().value}`}
                onSelect={() => props.onChange(item().value)}
              />
            )
            // ONE text, and the reason the group is disabled outranks the
            // option's own description: an option nobody can pick has nothing
            // to say about itself that beats why. Two nested Tooltips would
            // otherwise resolve the outer one's target to the inner one's
            // wrapper rather than to the button.
            const tip = () => (props.disabled ? props.disabledReason : item().tooltip)
            // Wrap only when there is tooltip text. A Tooltip mounts its own
            // wrapper and listeners even with nothing to show, and most option
            // values carry no description.
            return (
              <Show when={tip()} fallback={row()}>
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
          // A <span> receives pointer events, so <Tooltip> fires on it and the
          // reason keeps the app's own typography. `title` is reserved for a
          // control the browser has actually disabled.
          <Tooltip text={props.disabledReason ?? ''}>
            <span>
              {props.items.find(i => i.value === props.current)?.label || props.label}
            </span>
          </Tooltip>
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
