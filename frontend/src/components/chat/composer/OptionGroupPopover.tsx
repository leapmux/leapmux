import type { JSX } from 'solid-js'
import type { ProviderSettingChangeHandler } from '~/components/chat/providerSettings'
import type { SettingsItem } from '~/components/chat/settingsGroups'
import type { DropdownTriggerProps } from '~/components/common/DropdownMenu'
import type { AvailableOptionGroup } from '~/generated/proto/leapmux/v1/agent_pb'
import { createMemo, createSignal } from 'solid-js'
import { optionGroup, resolvedCurrent } from '~/components/chat/settingsGroups'
import { OptionGroupMenuItems } from '~/components/chat/settingsShared'
import { DropdownMenu } from '~/components/common/DropdownMenu'
import { shallowEqualArraysDeep } from '~/lib/shallowEqual'

/** What the trigger render-prop receives beyond the dropdown's own props. */
export interface OptionGroupTriggerView {
  /** The group's display label, falling back to its id. */
  label: string
  /** The label of the currently selected option (empty when unresolved). */
  currentLabel: string
  /** Whether the group accepts a change (false = agent-controlled). */
  mutable: boolean
}

/**
 * Props for {@link OptionGroupPopover}.
 */
export interface OptionGroupPopoverProps {
  /** Group id to render (e.g. `model`, `effort`, `permissionMode`). */
  groupId: string
  /** The full option-group catalog (used to resolve the group + its options). */
  optionGroups: AvailableOptionGroup[] | undefined
  /** Optimistic option-value map keyed by group id. */
  optionValues: Record<string, string>
  /** Dispatch a change for this axis. */
  onChange?: ProviderSettingChangeHandler
  /**
   * Why the composer accepts no changes at all, when it does not. Its PRESENCE
   * is what disables the options, so a dead surface can never render without
   * saying why. The caller resolves the sentence once and every surface shows
   * that one string -- the `[+]` menu's attach item, the editor's placeholder,
   * and each settings submenu -- instead of each inventing its own wording for
   * the same condition.
   */
  disabledReason?: string
  /** Renders the button that opens the popover. */
  trigger: (triggerProps: DropdownTriggerProps, view: OptionGroupTriggerView) => JSX.Element
  /** Class for the popover panel. */
  popoverClass?: string
  /** data-testid for the popover panel. */
  popoverTestId?: string
  /**
   * Test-id prefix for the option items, defaulting to the group id.
   *
   * Both settings surfaces can render the same group at the same time, so they
   * must not emit the same per-option ids — an unscoped locator would match
   * twice and be ambiguous for a test and for assistive tooling alike.
   */
  itemTestIdPrefix?: string
}

/**
 * One option group as a popover of selectable items, with a caller-supplied
 * trigger.
 *
 * Both settings surfaces render this: the status bar's per-axis chips and the
 * `[+]` menu's submenus. They differ only in the trigger and the popover's
 * class, so the group lookup, the item mapping, the selected-value resolution,
 * and the dispatch live here once and cannot drift between the two.
 */
export function OptionGroupPopover(props: OptionGroupPopoverProps): JSX.Element {
  // Whether this popover is open. A popover keeps its children mounted across a
  // close, so the filterable branch cannot tell a reopen from a re-render
  // without it: it would keep the filter text the user typed last time, and its
  // focus would have landed on a `display: none` input.
  const [open, setOpen] = createSignal(false)

  // A memo, not a plain thunk. `mergeStableOptionGroupRefs` keeps each group's
  // object identity but hands out a fresh ARRAY whenever any one group changes,
  // so a thunk would re-scan the catalog for every reader below (label, items,
  // readOnly, readOnlyReason) in EVERY popover -- both surfaces render one per
  // group -- on every status push. The memo's default `===` dedupes on the
  // preserved identity, so a push that leaves this group alone stops here.
  const group = createMemo(() => optionGroup(props.optionGroups, props.groupId))
  const label = () => group()?.label ?? props.groupId

  // The worker re-broadcasts the whole option catalog on every status push, and
  // a popover's children are rendered eagerly (they are in the DOM before it
  // opens), so without a value comparator every push would rebuild every row of
  // every group during streaming. Each item is built from one 3-key literal, so
  // the repo's shared shallow comparator is exact here.
  const items = createMemo<SettingsItem[]>(
    () => {
      const g = group()
      return g ? g.options.map(o => ({ label: o.name || o.id, value: o.id, tooltip: o.description || undefined })) : []
    },
    [],
    { equals: shallowEqualArraysDeep },
  )

  /**
   * The rows the MENU shows: the live list, held still while the popover is open.
   *
   * The catalog underneath is live, and a push can change the SET of options, not
   * only their objects -- the worker drops the CLI's resolved model into its
   * canonical slot, and the live CLI catalog replaces the static fallback list
   * wholesale a second or two after an agent starts. Every row below that change
   * then moves by a row height, under a pointer that is already aimed at one of
   * them, and the click applies the option that slid into its place. A click on
   * "Opus (1M context)" launched Fable 5 that way, which is the row directly
   * above it.
   *
   * Reading `open()` FIRST and returning without touching `items()` is what
   * freezes the list: that run subscribes to `open` alone, so no catalog push can
   * re-run this memo until the popover closes. The trigger's own label keeps
   * reading `items()` and stays live.
   *
   * The trade is one open of staleness: an option that appears while the menu is
   * open shows up the next time the user opens it. A picker that applies a value
   * the user did not choose is the worse failure -- it relaunches the agent on
   * the wrong model.
   */
  const shownItems = createMemo<SettingsItem[]>((prev) => {
    // `prev?.length`, not `prev`: an EMPTY list is nothing to hold still, and
    // freezing on one would leave a menu opened before its catalog arrived empty
    // until the user closed it again.
    if (open() && prev?.length)
      return prev
    return items()
  })

  /**
   * Apply a value the user picked, unless the live catalog dropped it.
   *
   * The other half of the freeze above. Holding the list still stops a row
   * SLIDING under the pointer, but the same push that adds an option can remove
   * one -- the live CLI catalog replaces the static fallback list wholesale, so
   * a fallback model it does not carry disappears. The frozen row for it is
   * still rendered and still wired, and applying it would relaunch the agent on
   * an id the provider rejects: the same failure the freeze exists to prevent,
   * reached from the other direction.
   *
   * A click that lands on a withdrawn option therefore does nothing but close
   * the menu, and the next open shows the list without it.
   */
  const applyIfStillOffered = (value: string) => {
    if (!items().some(it => it.value === value))
      return
    props.onChange?.({ sets: { [props.groupId]: value } })
  }

  const current = () => resolvedCurrent(props.optionGroups, props.optionValues, props.groupId)

  // A group the agent controls, or a composer that accepts no input at all,
  // both render the options read-only — the reasons differ, so the tooltip does.
  // The composer-wide reason comes from the caller, which resolved it once for
  // every surface; only the per-group reason is this component's to write.
  const readOnly = () => !!props.disabledReason || !group()?.mutable
  const readOnlyReason = () => {
    if (props.disabledReason)
      return props.disabledReason
    return group()?.mutable ? undefined : 'This setting is controlled by the agent'
  }

  return (
    <DropdownMenu
      trigger={triggerProps => props.trigger(triggerProps, {
        label: label(),
        currentLabel: items().find(i => i.value === current())?.label ?? '',
        mutable: !readOnly(),
      })}
      class={props.popoverClass}
      data-testid={props.popoverTestId}
      // The menu holds one named group of radio items, so it needs the group's
      // name. Without it assistive technology announces the values with nothing
      // that says which axis they set.
      aria-label={label()}
      onToggle={setOpen}
    >
      <OptionGroupMenuItems
        label={label()}
        items={shownItems()}
        testIdPrefix={props.itemTestIdPrefix ?? props.groupId}
        current={current()}
        onChange={value => applyIfStillOffered(value)}
        disabled={readOnly()}
        disabledReason={readOnlyReason()}
        openKey={open}
      />
    </DropdownMenu>
  )
}
