import type { JSX } from 'solid-js'
import type { ProviderSettingsAction, ProviderSettingsPanelProps, ProviderSettingsState } from './providers/registry'
import type { SettingsItem } from './settingsGroups'
import type { AvailableOptionGroup } from '~/generated/leapmux/v1/agent_pb'
import { createMemo, createUniqueId, For, Show } from 'solid-js'
import * as styles from './ChatView.css'
import {
  currentValueOrDefault,
  effectiveCurrent,
  OPTION_ID_EFFORT,
  OPTION_ID_MODEL,
  optionGroup,
  optionLabel,
  valueValidForGroup,
} from './settingsGroups'
import { effortIcon, OptionGroupSelect } from './settingsShared'

/**
 * Resolve the current value for an option group from the panel state. Prefer the
 * optimistically-updated value from the unified `optionValues` map (model/effort/
 * permission/extras alike, keyed by group id); fall back to the catalog group's
 * confirmed `currentValue` when no optimistic value was written. The fallback matters
 * for the action disabled-check below: an agent already in a target state via the
 * catalog (no optimistic mirror) must read as already-applied, not unset.
 */
function currentForGroup(state: ProviderSettingsState, groupId: string): string {
  return effectiveCurrent(state.optionValues, optionGroup(state.optionGroups, groupId))
}

/**
 * The value the panel actually shows selected for a group: the effective (optimistic-or-
 * confirmed) current, CLAMPED to the group's backend default when that current is unset or
 * out-of-list (currentValueOrDefault). Both the rendered selection and the action
 * disabled-check must compare against THIS -- reading the raw, unclamped currentForGroup
 * instead would leave an action enabled while the resolved-default state already matches its
 * target (e.g. a freshly-opened Codex agent's permission_mode before its first status push).
 */
function resolvedCurrentForGroup(state: ProviderSettingsState, groupId: string): string {
  return currentValueOrDefault(state.optionGroups, groupId, currentForGroup(state, groupId))
}

/** Groups sorted by their backend-provided display order (stable on ties). */
function sortedGroups(groups: AvailableOptionGroup[] | undefined): AvailableOptionGroup[] {
  return [...(groups ?? [])]
    .filter(g => g.options.length > 0)
    .sort((a, b) => a.order - b.order)
}

/**
 * One data-driven settings panel for every provider. Renders each
 * option group the agent reports (model, effort, permission mode, and any
 * provider-specific axis) in backend order; read-only (`mutable=false`) groups
 * are disabled; any group with more than the threshold of options becomes a
 * searchable list. Provider-declared `actions` (e.g. Codex "Bypass
 * permissions") render as buttons that set several groups at once.
 */
export function AgentSettingsPanel(props: ProviderSettingsPanelProps & { actions?: ProviderSettingsAction[] }): JSX.Element {
  const menuId = createUniqueId()
  const groups = () => sortedGroups(props.optionGroups)

  const dispatch = (groupKey: string, value: string) => props.onChange?.({ sets: { [groupKey]: value } })

  // Key the group list by stable id, NOT the group object. The worker re-broadcasts
  // the catalog on every status push, and any push that changes a field (e.g. a
  // group's currentValue as the agent settles, or the effort/thinking groups
  // recomputing) hands a `<For each={groups()}>` a fresh object for that group --
  // which would detach and recreate its entire DOM subtree, flickering the radios
  // and racing a click trying to land on one. Under load that settle window is long
  // enough to make the model picker effectively unclickable. Everything the render
  // needs (current, items, label, mutable) is read reactively from props by id, so
  // id-keying keeps each group's DOM stable across pushes while its contents update
  // in place -- the group-level analogue of RadioGroup's Index-keyed radios.
  const groupIds = () => groups().map(g => g.id)
  // Memoize an id->group index so each row's lookup is O(1): a plain `.find` per row
  // is O(n) and, run for every rendered group on every catalog push, O(n^2).
  const groupsById = createMemo(() => new Map(groups().map(g => [g.id, g])))
  const groupById = (id: string) => groupsById().get(id)

  return (
    <div class={styles.settingsPanelColumn}>
      <For each={groupIds()}>
        {(id) => {
          // createMemo so group() dedupes by ===: groupById returns the SAME object while this
          // group is unchanged (mergeStableOptionGroupRefs), so items below recomputes only when
          // THIS group changes -- not on every push that settles some OTHER group.
          const group = createMemo(() => groupById(id))
          const current = () => resolvedCurrentForGroup(props, id)
          // Build the item list only when the group object changes. Recomputing it on every push
          // would hand the searchable list's identity-keyed <For> a fresh array of fresh objects,
          // detaching and recreating every row (the same flicker/click-race the id-keyed groups
          // and Index-keyed radios avoid). The map is inlined here (its lone caller), reusing the
          // group already resolved above instead of re-looking it up.
          const items = createMemo<SettingsItem[]>(() => {
            const g = group()
            return g ? g.options.map(o => ({ label: o.name || o.id, value: o.id, tooltip: o.description || undefined })) : []
          })
          return (
            <Show when={group()}>
              {g => (
                <OptionGroupSelect
                  label={g().label || id}
                  items={items()}
                  testIdPrefix={id}
                  name={`${menuId}-${id}`}
                  current={current()}
                  onChange={v => dispatch(id, v)}
                  disabled={!g().mutable}
                  disabledReason={g().mutable ? undefined : 'This setting is controlled by the agent'}
                />
              )}
            </Show>
          )
        }}
      </For>
      <For each={props.actions ?? []}>
        {action => (
          <button
            class="outline small"
            style={{ 'margin-bottom': 'var(--space-2)' }}
            data-testid={action.testId}
            // Compare each target against the SAME value the radios show selected
            // (resolvedCurrentForGroup clamps an unset/out-of-list current to the backend default),
            // so the action reads as already-applied when the resolved state already matches.
            disabled={Object.entries(action.sets).every(([k, v]) => resolvedCurrentForGroup(props, k) === v)}
            // Dispatch all axes as ONE change so the worker applies them atomically -- a
            // partial failure (e.g. Codex bypass setting approval but not sandbox) can't leave
            // the agent half-applied while the optimistic UI shows the full state.
            onClick={() => props.onChange?.({ sets: { ...action.sets } })}
          >
            {action.label}
          </button>
        )}
      </For>
    </div>
  )
}

/**
 * Placeholder shown in the trigger's model slot while the model is unresolved.
 * Copilot, OpenCode, and Goose discover their models dynamically from
 * `session/new`, so the model option group is absent until the handshake
 * completes; without this the trigger would render a dangling "· Mode". It
 * resolves to the real model name once the agent reports one, and persists for
 * an agent that never advertises a model (a server with no model list, or one
 * that died before its first catalog reached the client).
 */
const UNRESOLVED_MODEL_PLACEHOLDER = '…'

/**
 * The agent settings-dropdown trigger label, driven by well-known option ids:
 * the current model name, an effort icon (when an effort axis exists and the
 * current value is valid), and the current permission-mode label. Replaces the
 * per-provider trigger labels.
 */
export function AgentSettingsPanelTriggerLabel(props: ProviderSettingsPanelProps & { triggerModeGroupKey?: string }): JSX.Element {
  // The trigger LAYOUT references the well-known ids to lay out its model -> effort
  // -> mode segments (a deliberate, designed order); the stored VALUES all come from
  // currentForGroup -- the SAME reader the panel uses -- so the trigger and panel agree
  // by construction (one resolution path) rather than only because the caller happens to
  // seed optionValues from the already-overlaid groups.
  const valueFor = (id: string) => currentForGroup(props, id)
  // Treat a zero-option group as absent, matching the panel's sortedGroups filter (which drops
  // empty groups). Otherwise the trigger could render a "· Mode" segment for a group the panel
  // refuses to render -- a trigger/panel disagreement for a transiently empty group.
  const hasGroup = (id: string) => {
    const g = optionGroup(props.optionGroups, id)
    return g != null && g.options.length > 0
  }
  const hasEffort = () => hasGroup(OPTION_ID_EFFORT)
  const modelLabel = () => optionLabel(props.optionGroups, OPTION_ID_MODEL, valueFor(OPTION_ID_MODEL))
  // Always render the model slot -- the resolved name, or a placeholder while the
  // model group is still absent (pre-handshake) or its value unresolved. This keeps
  // the trigger reading "… · Mode" instead of a dangling, model-less "· Mode".
  const modelText = () => modelLabel() || UNRESOLVED_MODEL_PLACEHOLDER
  const effortValid = () => valueValidForGroup(props.optionGroups, OPTION_ID_EFFORT, valueFor(OPTION_ID_EFFORT))

  // The mode segment shows the current value of the ONE option group the provider
  // declares as its mode axis (permissionMode for Claude/Cursor/Copilot/Goose, the
  // collaboration_mode "Workflow" group for Codex, primaryAgent for OpenCode/Kilo).
  // Sourcing from a single declared group -- instead of fusing planMode and
  // permissionMode -- keeps this provider-agnostic, naturally surfaces the plan
  // label when that group sits at its plan value, and shows OpenCode/Kilo's
  // primaryAgent (which the old planMode-or-permissionMode logic hid whenever it
  // wasn't at the plan value). Returns '' when the provider has no mode axis or the
  // group is absent, so -- like the model slot -- the " · " separator never dangles.
  const modeLabel = () => {
    const key = props.triggerModeGroupKey
    if (!key || !hasGroup(key))
      return ''
    return optionLabel(props.optionGroups, key, valueFor(key))
  }

  return (
    <>
      {modelText()}
      <Show when={hasEffort() && effortValid()}>{effortIcon(valueFor(OPTION_ID_EFFORT))}</Show>
      <Show when={modeLabel()}>
        {' · '}
        {modeLabel()}
      </Show>
    </>
  )
}
