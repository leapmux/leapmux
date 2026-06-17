import type { AvailableOptionGroup } from '~/generated/leapmux/v1/agent_pb'
import { equals } from '@bufbuild/protobuf'
import { AvailableOptionGroupSchema } from '~/generated/leapmux/v1/agent_pb'

// Pure, render-free helpers over the generic option-group catalog. Kept in a `.ts`
// module (not the `.tsx` component file) so non-rendering consumers -- tab.helpers,
// useAgentOperations, useWorkspaceConnection, registerACPProvider -- can import them
// without transitively pulling in Solid components and lucide icons. The UI widgets and
// icon helpers live in ./settingsShared.

/** Well-known option-group ids shared with the backend. */
export const OPTION_ID_MODEL = 'model' as const
export const OPTION_ID_EFFORT = 'effort' as const
export const OPTION_ID_PERMISSION_MODE = 'permissionMode' as const

/**
 * Leapmux-side sentinel meaning "let the CLI pick its own default reasoning effort". The
 * backend omits --effort (Claude) / reasoning_effort (Codex) when an agent carries this value,
 * so older CLIs that don't recognize newer effort names (e.g. "xhigh") still work. Mirrors
 * `agent.EffortAuto` in the Go worker. Every catalog-effort model offers it, so it is the safe
 * fallback when a transient catalog reports no concrete effort default.
 */
export const EFFORT_AUTO = 'auto' as const

// The account-default model sentinel: the only model value a relaunch resolves to a
// DIFFERENT concrete model (the CLI picks the account default). A concrete model id
// the user explicitly picked stays itself, so only this value needs the post-switch
// reconcile to snap to the settled catalog. Mirrors the backend DefaultModelSentinel.
export const ACCOUNT_DEFAULT_MODEL = 'default' as const

/** Threshold above which an option group renders as a searchable list instead of radios. */
export const OPTION_GROUP_SEARCHABLE_THRESHOLD = 7

/** Shared item type used by RadioGroup and settings helpers. */
export interface SettingsItem {
  label: string
  value: string
  /** Hover text shown by RadioGroup and the searchable list. */
  tooltip?: string
}

/** Find an option group by id. */
export function optionGroup(optionGroups: AvailableOptionGroup[] | undefined, id: string) {
  return optionGroups?.find(g => g.id === id)
}

/**
 * The effective current value for an option group: the optimistically-updated value from
 * `optionValues` (keyed by group id) if set, else the catalog group's confirmed
 * `currentValue`. The two are equivalent only because `optionValues` never stores an empty
 * string (setOptionValue deletes the key on empty), so `||` is never fooled by a stored "".
 * Centralizing the precedence keeps the panel's read (currentForGroup) and the store's
 * overlay projection (agentTabOptionGroups) from drifting.
 */
export function effectiveCurrent(optionValues: Record<string, string> | undefined, group: AvailableOptionGroup | undefined): string {
  const id = group?.id ?? ''
  const stored = optionValues?.[id]
  if (stored === '') {
    // Invariant violation: optionValues must never store an empty string (setOptionValue
    // deletes the key on empty). An empty stored value means a raw write bypassed
    // setOptionValue, and the `||` below would silently fall through to the catalog current as
    // if the override didn't exist -- masking a deliberate clear. Warn so the bug surfaces.
    console.warn(`effectiveCurrent: option "${id}" has an empty stored value (should be deleted, not stored as "")`)
  }
  return stored || group?.currentValue || ''
}

/**
 * Build a plan-mode `currentMode` reader: `agent => agent.optionValues?.[groupKey] ||
 * defaultValue`. Every provider's plan-mode config derives its current value the same way
 * from its own group key + non-plan default, so this keeps the three definitions (Claude,
 * Codex, ACP) from re-implementing -- and drifting on -- the same expression.
 */
export function currentModeFor(groupKey: string, defaultValue: string): (agent: { optionValues?: Record<string, string> }) => string {
  return agent => agent.optionValues?.[groupKey] || defaultValue
}

/**
 * Build a provider's `planMode` config from its group key, plan value, and non-plan default.
 * Every provider (Claude, Codex, ACP) stores the plan toggle as a value in `optionValues`
 * under a group key, differing only in which key/value -- so this assembles the whole 4-field
 * shape in one place. Threading `groupKey`/`defaultValue` through `currentModeFor` here makes
 * it mechanically impossible for the `currentMode` reader's key/default to drift from the
 * sibling fields, which an inline literal spelled each of them twice and could.
 */
export function buildPlanMode(groupKey: string, planValue: string, defaultValue: string) {
  return {
    groupKey,
    currentMode: currentModeFor(groupKey, defaultValue),
    planValue,
    defaultValue,
  }
}

/**
 * Reuse each previous group's object reference for any group in `next` whose
 * content is unchanged (matched by id), returning `prev` outright when nothing
 * changed. The worker re-decodes and re-broadcasts the whole catalog on every
 * status push, so a single changed group (e.g. effort after picking a tier)
 * would otherwise hand every other group a fresh reference too -- recreating
 * their `<For>`/`<Index>` rows and racing a click on, say, the unchanged model
 * radio. Per-group reference stability keeps the untouched groups' DOM intact
 * while still surfacing the one that changed.
 */
export function mergeStableOptionGroupRefs(next: AvailableOptionGroup[], prev: AvailableOptionGroup[]): AvailableOptionGroup[] {
  if (next === prev)
    return next
  const prevById = new Map(prev.map(g => [g.id, g]))
  const out = next.map((g) => {
    const old = prevById.get(g.id)
    return old && equals(AvailableOptionGroupSchema, g, old) ? old : g
  })
  // Whole-array reuse when every group (and the ordering) is unchanged.
  if (out.length === prev.length && out.every((g, i) => g === prev[i]))
    return prev
  return out
}

/** Resolve the display label for an option-group id. */
export function optionGroupLabel(optionGroups: AvailableOptionGroup[] | undefined, id: string): string {
  const group = optionGroup(optionGroups, id)
  return group?.label || id
}

/** Resolve the provider-default option id for a group (its backend `defaultValue`). */
export function optionGroupDefaultValue(optionGroups: AvailableOptionGroup[] | undefined, id: string): string {
  const group = optionGroup(optionGroups, id)
  if (!group || group.options.length === 0)
    return ''
  // Only the backend-declared defaultValue is a trustworthy default. Do NOT fall back to
  // options[0]: effort / thought_level groups are sorted strongest-first, so the first
  // option is the MOST aggressive tier -- guessing it would silently preselect e.g.
  // "xhigh" for a group whose default the server hasn't reported (a transient empty
  // current on first handshake). An empty result lets the select render unselected
  // rather than wrong.
  return group.defaultValue || ''
}

/** Resolve a human-readable label for a value within an option group. */
export function optionLabel(optionGroups: AvailableOptionGroup[] | undefined, id: string, currentValue: string): string {
  const group = optionGroup(optionGroups, id)
  const opt = group?.options.find(o => o.id === currentValue)
  return opt?.name || opt?.id || currentValue
}

/** Whether `value` is one of the options the group currently offers. */
export function valueValidForGroup(optionGroups: AvailableOptionGroup[] | undefined, id: string, value: string): boolean {
  return optionGroup(optionGroups, id)?.options.some(o => o.id === value) ?? false
}

/**
 * The current value if the group still offers it, else the group's default.
 * Guards a select from rendering with no selection during an optimistic model
 * switch, where the effort can briefly be a tier the new model doesn't offer
 * (e.g. "ultracode"/"xhigh" left over from Opus after switching to Sonnet). The
 * group's default is the new model's default tier, which is also what the backend
 * settles on (it resets effort to auto, and the relaunched session resolves auto
 * to that default) -- so this fallback matches the settled value, no flash.
 *
 * When the group reports no concrete default (a transient empty default on the
 * first handshake for a model), the effort group falls back to EFFORT_AUTO -- the
 * always-safe "let the model pick" tier the backend settles on -- rather than
 * rendering unselected, but only when the group actually offers it (some ACP
 * effort axes have no 'auto' level). The MODEL group falls back to its first option:
 * a dynamic-model ACP provider (Copilot/OpenCode/Goose) can report a model list before
 * any current/default model resolves, and unlike effort/thought tiers (sorted
 * strongest-first, where guessing options[0] would preselect the most aggressive tier)
 * the model list has no dangerous first entry -- it is the catalog's most-preferred model,
 * so showing it beats rendering the picker blank. Other groups keep the blank-over-wrong
 * behavior, since their first option may be a meaningful non-default selection.
 */
export function currentValueOrDefault(optionGroups: AvailableOptionGroup[] | undefined, id: string, value: string): string {
  if (valueValidForGroup(optionGroups, id, value))
    return value
  const def = optionGroupDefaultValue(optionGroups, id)
  if (def)
    return def
  if (id === OPTION_ID_EFFORT && valueValidForGroup(optionGroups, id, EFFORT_AUTO))
    return EFFORT_AUTO
  if (id === OPTION_ID_MODEL)
    return optionGroup(optionGroups, id)?.options[0]?.id ?? ''
  return ''
}

/** Context window (tokens) of the currently-selected model, or 0 if unknown. */
export function selectedModelContextWindow(optionGroups: AvailableOptionGroup[] | undefined, modelId: string): number {
  const opt = optionGroup(optionGroups, OPTION_ID_MODEL)?.options.find(o => o.id === modelId)
  return Number(opt?.contextWindow ?? 0)
}
