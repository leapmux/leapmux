import type { PillOptions } from '~/components/common/PillGroup'
import { isPillOptions, PILL_OPTION_LIMIT } from '~/components/common/PillGroup'

export interface WirePermissionOption {
  optionId: string
  kind: string
  /** Absent on wire payloads that omit it; every reader must tolerate `undefined`. */
  name?: string
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

/** The option family the request's positive action sends: only these apply a permission preset. */
export function isAllowPermissionKind(kind: string): boolean {
  return kind === KIND_ALLOW_ONCE || kind === KIND_ALLOW_ALWAYS
}

/** Goose sets every option's name to its kind (`name === optionId === kind`), which is no label at all. */
const KIND_FALLBACK_LABELS: Record<string, string> = {
  [KIND_ALLOW_ONCE]: 'Allow once',
  [KIND_ALLOW_ALWAYS]: 'Allow always',
  [KIND_REJECT_ONCE]: 'Reject',
  [KIND_REJECT_ALWAYS]: 'Reject always',
}

/** The label an extra option's button shows: the agent's own name, unless the name is just the id. */
export function permissionOptionLabel(option: WirePermissionOption): string {
  if (option.name !== undefined && option.name !== option.optionId)
    return option.name
  return KIND_FALLBACK_LABELS[option.kind] ?? option.name ?? option.optionId
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
 * - the decision buttons carry the polarity alone (Deny / Allow) while a scope
 *   pill group states HOW LONG an allow lasts; a slot that holds a remember
 *   option with no scope group to qualify it states its own duration instead
 *   (see `decisionLabel`);
 * - one allow-once beside one or more allow-always options becomes the
 *   `allowScope` group — Once plus each always scope, once first then payload
 *   order — while the group fits the pill limit: two options draw
 *   [Once | Always] (or [Once | Session], per the agent's own option names), three
 *   draw Reasonix's [Once | Session | Project], a wider vocabulary degrades to
 *   no group and plain extra buttons;
 * - a scope beyond Once also upgrades Deny to the agent's reject_always when it
 *   offers one (goose is the one agent that does), so one control answers both
 *   polarities;
 * - the FIRST option of each canonical kind fills that kind's slot; every
 *   option no rendered slot or group consumed (invented kinds, duplicates
 *   beyond the first of a kind, and every option a degraded layout cannot
 *   place) stays in `additional`, payload order, so no answerable option is
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
  /** The reject_always slot, when the agent offers one beside a reject_once AND a scope group is drawn. */
  rememberReject?: WirePermissionOption
  /**
   * The allow options a scope pill group offers — one allow-once plus every
   * allow-always, once first then payload order. Undefined when the agent
   * offers no always scope at all, no single once slot to anchor the group, or
   * a vocabulary too wide for the pill limit.
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

  // The scope group needs ONE once slot facing at least one always scope (a
  // Once pill is ambiguous when two once answers exist — Cursor routes its
  // ask-question options that way, and those are alternative answers, not
  // durations), it must fit the pill limit (a group this layout reports is one
  // the pills can draw, so a too-wide vocabulary degrades HERE and its options
  // stay answerable as extra buttons), and its ids must be unique (the reply
  // carries an id, so two options that share one are the same answer twice).
  const allOnces = options.filter(option => option.kind === KIND_ALLOW_ONCE)
  const allAlways = options.filter(option => option.kind === KIND_ALLOW_ALWAYS)
  const once = allOnces.length === 1 ? allOnces[0] : undefined
  const scopeAlways: WirePermissionOption[] = []
  if (once) {
    for (const option of allAlways) {
      if (option.optionId !== once.optionId && !scopeAlways.some(seen => seen.optionId === option.optionId))
        scopeAlways.push(option)
    }
  }
  const allowScope = once && scopeAlways.length >= 1 && 1 + scopeAlways.length <= PILL_OPTION_LIMIT
    ? [once, ...scopeAlways]
    : undefined

  // A slot or group is consumed only when the row actually renders it: with a
  // scope group drawn, its members plus BOTH reject slots are consumed (Deny
  // sends the once reject; a scope beyond Once upgrades Deny to reject_always).
  // Without one there is no reject upgrade, so reject_always stays answerable
  // as its own extra button.
  const positive = allowOnce ?? allowAlways
  const negative = rejectOnce ?? rejectAlways
  const consumed = new Set<WirePermissionOption | undefined>(allowScope ?? [positive, negative])
  if (allowScope) {
    consumed.add(rejectOnce)
    consumed.add(rejectAlways)
  }

  return {
    positive,
    negative,
    rememberReject: allowScope && rejectOnce && rejectAlways ? rejectAlways : undefined,
    allowScope,
    additional: options.filter(option => !consumed.has(option)),
  }
}

/**
 * Whether the selected scope pill picks a scope BEYOND Once. This is what
 * upgrades a reject to the agent's reject_always, so the two polarities answer
 * through one control.
 */
function scopeRemembers(
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
 * reject_always when it offers one.
 *
 * An option the agent did not offer is never sent: the reject upgrade is
 * skipped when its variant is absent, and an unknown optionId is parsed as
 * cancel (goose) or reject (OpenCode) on the agent side.
 */
export function resolvePermissionOption(
  layout: PermissionOptionLayout,
  polarity: 'allow' | 'reject',
  selectedAllowScopeId?: string,
): WirePermissionOption | undefined {
  if (polarity === 'allow') {
    if (layout.allowScope) {
      const selected = layout.allowScope.find(option => option.optionId === selectedAllowScopeId)
      return selected ?? layout.allowScope[0]
    }
    return layout.positive
  }
  return scopeRemembers(layout, selectedAllowScopeId) && layout.rememberReject ? layout.rememberReject : layout.negative
}

/**
 * The label a decision button shows. With a scope pill group drawn the button
 * carries the polarity alone — the pills state how long an allow lasts. Without
 * one, a slot that holds a REMEMBER option states its own duration through the
 * agent's own option name (`permissionOptionLabel`): the agent offered no once
 * variant, so the button is the only place the duration can appear, and a plain
 * "Allow" would grant a permanent permission the user cannot see.
 */
export function decisionLabel(layout: PermissionOptionLayout, polarity: 'allow' | 'reject'): string {
  const slot = polarity === 'allow' ? layout.positive : layout.negative
  const rememberKind = polarity === 'allow' ? KIND_ALLOW_ALWAYS : KIND_REJECT_ALWAYS
  return !layout.allowScope && slot?.kind === rememberKind ? permissionOptionLabel(slot) : polarity === 'allow' ? 'Allow' : 'Deny'
}

/**
 * One scope pill's label. The once slot is unambiguous; the always scopes read
 * their duration out of the agent's own option name ("...for this session",
 * "Add to project allow_write"), and a name that states no duration is simply
 * Always. Names are prose, so this is a keyword read, not a parse — a name with
 * neither keyword still gets a truthful label, just a generic one.
 */
export function allowScopeLabel(option: WirePermissionOption): string {
  if (option.kind === KIND_ALLOW_ONCE)
    return 'Once'
  const name = (option.name ?? '').toLowerCase()
  if (name.includes('project'))
    return 'Project'
  if (name.includes('session'))
    return 'Session'
  return 'Always'
}

/**
 * The scope pill group's options, keyed by optionId so a selection maps
 * straight onto the wire reply. Undefined when the scope group cannot be drawn
 * (fewer than one pill, or more than the pill limit) — the caller then leaves
 * the scope to the plain Allow button. Two scopes the keyword read cannot tell
 * apart (both read "Always") show their own names instead, so no two pills of
 * one group share a label the user cannot distinguish.
 */
export function allowScopePillOptions(scope: readonly WirePermissionOption[]): PillOptions<string> | undefined {
  const labels = scope.map(option => allowScopeLabel(option))
  const counts = new Map<string, number>()
  for (const label of labels)
    counts.set(label, (counts.get(label) ?? 0) + 1)
  const pills = scope.map((option, index) => ({
    key: option.optionId,
    label: (counts.get(labels[index]) ?? 0) > 1 ? permissionOptionLabel(option) : labels[index]!,
  }))
  return isPillOptions(pills) ? pills : undefined
}
