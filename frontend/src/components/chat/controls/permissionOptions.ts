import type { PillOptions, PillOptionSpec } from '~/components/common/PillGroup'

import { PILL_OPTION_LIMIT } from '~/components/common/PillGroup'

export interface WirePermissionOption {
  optionId: string
  kind: string
  name: string
}

const KIND_ALLOW_ONCE = 'allow_once'
const KIND_ALLOW_ALWAYS = 'allow_always'
const KIND_REJECT_ONCE = 'reject_once'
const KIND_REJECT_ALWAYS = 'reject_always'

const CANONICAL_KINDS = [KIND_ALLOW_ONCE, KIND_ALLOW_ALWAYS, KIND_REJECT_ONCE, KIND_REJECT_ALWAYS]

/** The answer-state key the allow-scope pill group's selection is stored under. */
export const ALLOW_SCOPE_CHOICE_ID = 'control-allow-scope-pill'

export function isRejectPermissionKind(kind: string): boolean {
  return kind === KIND_REJECT_ONCE || kind === KIND_REJECT_ALWAYS
}

/** Goose names every option after its kind (`name === optionId === kind`), which is no label at all. */
const KIND_FALLBACK_LABELS: Record<string, string> = {
  [KIND_ALLOW_ONCE]: 'Allow once',
  [KIND_ALLOW_ALWAYS]: 'Allow always',
  [KIND_REJECT_ONCE]: 'Reject',
  [KIND_REJECT_ALWAYS]: 'Reject always',
}

/** The label an extra option's button shows: the agent's own name, unless the name is just the id. */
export function permissionOptionLabel(option: WirePermissionOption): string {
  return option.name === option.optionId
    ? KIND_FALLBACK_LABELS[option.kind] ?? option.name
    : option.name
}

/**
 * How a permission request's options lay out as one decision row.
 *
 * Agents emit their remember semantics as DISTINCT options (an `allow_always`
 * kind sits beside `allow_once`, not inside it), and the optionId vocabulary is
 * agent-specific (`allow_always`, `always`, `allow-always`,
 * `reasonix_write_session`, ...). The kinds are the only stable discriminator,
 * so this mapping classifies by kind:
 *
 * - the decision buttons carry the polarity alone (Deny / Allow); HOW LONG an
 *   allow lasts is the scope pill group's question, not the button's;
 * - one allow-once beside one or more allow-always options becomes the
 *   `allowScope` group — Once plus each always scope, payload order — whatever
 *   their number: two options draw [Once | Always] (or [Once | Session], per
 *   the agent's own naming), three draw Reasonix's [Once | Session | Project];
 * - a scope beyond Once also upgrades Deny to the agent's reject_always when it
 *   offers one (goose is the one agent that does) — the one behavior the
 *   Remember switch this group replaced carried across both polarities;
 * - the FIRST option of each canonical kind fills that kind's slot; every
 *   option no slot consumed (invented kinds, and duplicates beyond the first of
 *   a kind) stays in `additional`, payload order, so no answerable option is
 *   ever dropped.
 *
 * The BUTTON ORDER is this layout's own (negative first), not the payload's:
 * every agent emits allow-first, but the reply carries only an optionId, so
 * ordering is presentation-only — goose's own desktop client picks options by
 * kind, and goose's server parses the returned id string.
 */
export interface PermissionOptionLayout {
  negative?: WirePermissionOption
  /** The option Allow sends while no scope control overrides it (the once slot, or the only allow). */
  positive?: WirePermissionOption
  /** The reject_always slot, when the agent offers one beside a reject_once. */
  rememberReject?: WirePermissionOption
  /**
   * The allow options a scope pill group offers — one allow-once plus every
   * allow-always, once first then payload order. Undefined when the agent
   * offers no always scope at all, or no single once slot to anchor the group.
   */
  allowScope?: WirePermissionOption[]
  additional: WirePermissionOption[]
}

export function layoutPermissionOptions(options: WirePermissionOption[]): PermissionOptionLayout {
  const byKind = new Map<string, WirePermissionOption>()
  for (const option of options) {
    if (CANONICAL_KINDS.includes(option.kind) && !byKind.has(option.kind))
      byKind.set(option.kind, option)
  }
  const allowOnce = byKind.get(KIND_ALLOW_ONCE)
  const allowAlways = byKind.get(KIND_ALLOW_ALWAYS)
  const rejectOnce = byKind.get(KIND_REJECT_ONCE)
  const rejectAlways = byKind.get(KIND_REJECT_ALWAYS)

  // The scope group needs ONE once slot facing at least one always scope.
  // Requiring exactly one allow_once keeps payloads with several (Cursor
  // routes its ask-question options that way, and a Once pill is ambiguous
  // when two once answers exist) out of the scope vocabulary — those are
  // alternative answers, not durations.
  const allOnces = options.filter(option => option.kind === KIND_ALLOW_ONCE)
  const allAlways = options.filter(option => option.kind === KIND_ALLOW_ALWAYS)
  const allowScope = allOnces.length === 1 && allAlways.length >= 1
    ? [allOnces[0]!, ...allAlways]
    : undefined
  // The reject slots are consumed whether or not a scope group is drawn: a
  // reject option a decision button already sends is not an extra.
  const consumed = new Set([...(allowScope ?? [allowOnce, allowAlways]), rejectOnce, rejectAlways])

  return {
    positive: allowOnce ?? allowAlways,
    negative: rejectOnce ?? rejectAlways,
    rememberReject: rejectOnce && rejectAlways ? rejectAlways : undefined,
    allowScope,
    additional: options.filter(option => !consumed.has(option)),
  }
}

/**
 * Whether the selected scope pill picks a scope BEYOND Once — the pill-group
 * reading of the Remember switch this group replaced. This is what upgrades a
 * reject to the agent's reject_always, so the two polarities keep sharing one
 * control exactly as the switch made them share one checkbox.
 */
export function scopeRemembers(
  layout: PermissionOptionLayout,
  selectedAllowScopeId?: string,
): boolean {
  const scope = layout.allowScope
  if (!scope)
    return false
  const selected = scope.find(option => option.optionId === selectedAllowScopeId)
  return selected !== undefined && selected.kind === KIND_ALLOW_ALWAYS
}

/**
 * The option a decision button sends.
 *
 * For Allow, the SELECTED scope pill when a scope group is drawn (falling back
 * to its first option when nothing valid is stored), else the once slot the
 * button displays. For Reject, a scope beyond Once upgrades to the agent's
 * reject_always (`scopeRemembers` is that reading) when it offers one.
 *
 * An option the agent did not offer is never sent: the reject upgrade is
 * skipped when its variant is absent, and an unknown optionId is parsed as
 * cancel (goose) or reject (OpenCode) on the agent side.
 */
export function resolvePermissionOption(
  layout: PermissionOptionLayout,
  polarity: 'allow' | 'reject',
  remember: boolean,
  selectedAllowScopeId?: string,
): WirePermissionOption | undefined {
  if (polarity === 'allow') {
    if (layout.allowScope) {
      const selected = layout.allowScope.find(option => option.optionId === selectedAllowScopeId)
      return selected ?? layout.allowScope[0]
    }
    return layout.positive
  }
  return remember && layout.rememberReject ? layout.rememberReject : layout.negative
}

/**
 * One scope pill's label. The once slot is unambiguous; the always scopes read
 * their duration out of the agent's own option name ("...for this session",
 * "Add to project allow_write"), and a name that names no duration is simply
 * Always. Names are prose, so this is a keyword read, not a parse — a name with
 * neither keyword still gets a truthful label, just a generic one.
 */
export function allowScopeLabel(option: WirePermissionOption): string {
  if (option.kind === KIND_ALLOW_ONCE)
    return 'Once'
  const name = option.name.toLowerCase()
  if (name.includes('project'))
    return 'Project'
  if (name.includes('session'))
    return 'Session'
  return 'Always'
}

function isPillOptions(options: readonly PillOptionSpec<string>[]): options is PillOptions<string> {
  return options.length > 0 && options.length <= PILL_OPTION_LIMIT
}

/**
 * The scope pill group's options, keyed by optionId so a selection maps
 * straight onto the wire reply. Undefined when the scope group cannot be drawn
 * (fewer than one pill, or more than the pill limit) — the caller then leaves
 * the scope to the plain Allow button.
 */
export function allowScopePillOptions(scope: readonly WirePermissionOption[]): PillOptions<string> | undefined {
  const pills = scope.map(option => ({ key: option.optionId, label: allowScopeLabel(option) }))
  return isPillOptions(pills) ? pills : undefined
}
