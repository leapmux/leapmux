import type { PermissionOption, PermissionScope } from '~/components/chat/model/controlPrompt'
import type { PillOptions } from '~/components/common/PillGroup'
import { CANONICAL_KINDS, KIND_ALLOW_ALWAYS, KIND_ALLOW_ONCE, KIND_REJECT_ALWAYS, KIND_REJECT_ONCE } from '~/components/chat/model/controlPrompt'
import { disambiguateLabels, isPillOptions, PILL_OPTION_LIMIT } from '~/components/common/PillGroup'
import { permissionOptionLabel } from './permissionOptionLabels'

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
 *   pill group states HOW LONG the answer lasts; a slot that holds a remember
 *   option states its own duration instead, because the pills state the
 *   duration of a once answer only (see `decisionLabel`);
 * - one allow-once beside one or more allow-always options becomes the
 *   `allowScope` group — Once plus each always scope, once first then payload
 *   order — while the group fits the pill limit: two options draw
 *   [Once | Always] (or [Once | Session], per the agent's own option names), three
 *   draw Reasonix's [Once | Session | Project], a wider vocabulary degrades to
 *   no group and plain extra buttons;
 * - a scope beyond Once also upgrades Deny to the agent's reject_always when it
 *   offers one, so one control answers both polarities. An agent that states
 *   the scope of each option (see `PermissionOption.scope`) gets the reject of
 *   the selected scope;
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
  negative?: PermissionOption
  /** The option Allow sends while no scope control overrides it (the once slot, or the only allow). */
  positive?: PermissionOption
  /**
   * The reject_always options that Deny can send, when the agent offers one beside
   * a reject_once AND a scope group is drawn (see `reachableRejects`). Absent when
   * no pill reaches a reject_always.
   */
  rememberRejects?: PermissionOption[]
  /**
   * The allow options a scope pill group offers — one allow-once plus every
   * allow-always, once first then payload order. Undefined when the agent
   * offers no always scope at all, no single once slot to anchor the group, or
   * a vocabulary too wide for the pill limit.
   */
  allowScope?: PermissionOption[]
  additional: PermissionOption[]
}

export function layoutPermissionOptions(options: PermissionOption[]): PermissionOptionLayout {
  const byKind = new Map<string, PermissionOption>()
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
  const scopeAlways: PermissionOption[] = []
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
  // scope group drawn, its members, the reject that Deny sends, and each
  // reject_always that a pill upgrades Deny to are consumed. Without one there is
  // no reject upgrade, so reject_always stays answerable as its own extra button.
  // So does a reject_always that no pill reaches.
  const positive = allowOnce ?? allowAlways
  const negative = rejectOnce ?? rejectAlways
  const reachable = allowScope && rejectOnce ? reachableRejects(options, allowScope) : []
  const rememberRejects = reachable.length > 0 ? reachable : undefined
  const consumed = new Set<PermissionOption | undefined>(allowScope ?? [positive, negative])
  if (allowScope) {
    consumed.add(negative)
    for (const option of reachable)
      consumed.add(option)
  }

  // The optional slots are set only when they hold an option: an explicit
  // `undefined` is not assignable to an optional prop, and every reader treats
  // absent the same.
  const layout: PermissionOptionLayout = {
    additional: options.filter(option => !consumed.has(option)),
  }
  if (positive)
    layout.positive = positive
  if (negative)
    layout.negative = negative
  if (rememberRejects)
    layout.rememberRejects = rememberRejects
  if (allowScope)
    layout.allowScope = allowScope
  return layout
}

/**
 * The reject_always options that a pill of the scope group reaches, in payload
 * order (see `rememberRejectFor`).
 *
 * - A reject that states no scope answers each always pill.
 * - A reject that states the scope of an always pill answers that pill.
 * - The first reject of each scope counts, and so does the first that states no
 *   scope. A later one reaches no pill.
 *
 * A reject that reaches no pill stays an extra button, so it stays answerable.
 */
function reachableRejects(options: readonly PermissionOption[], allowScope: readonly PermissionOption[]): PermissionOption[] {
  const pillScopes = new Set<PermissionScope>()
  for (const option of allowScope) {
    if (option.kind === KIND_ALLOW_ALWAYS && option.scope !== undefined)
      pillScopes.add(option.scope)
  }
  const seen = new Set<PermissionScope | undefined>()
  const rejects: PermissionOption[] = []
  for (const option of options) {
    if (option.kind !== KIND_REJECT_ALWAYS || seen.has(option.scope))
      continue
    if (option.scope !== undefined && !pillScopes.has(option.scope))
      continue
    seen.add(option.scope)
    rejects.push(option)
  }
  return rejects
}

/**
 * The selected scope pill when it picks a scope BEYOND Once. This is what
 * upgrades a reject to the agent's reject_always, so the two polarities answer
 * through one control.
 */
function rememberingScope(
  layout: PermissionOptionLayout,
  selectedAllowScopeId?: string,
): PermissionOption | undefined {
  const selected = layout.allowScope?.find(option => option.optionId === selectedAllowScopeId)
  return selected?.kind === KIND_ALLOW_ALWAYS ? selected : undefined
}

/**
 * The reject_always that answers one always pill.
 *
 * - A reject that states the pill's scope answers it.
 * - Else a reject that states no scope answers it, as the agent's one remember
 *   answer. An agent that states no scope has only this case.
 * - A reject that states a different scope never answers it: Deny would then keep
 *   a rule at a scope that the reader did not select. Deny sends the once reject.
 */
function rememberRejectFor(rejects: readonly PermissionOption[], pill: PermissionOption): PermissionOption | undefined {
  return (pill.scope !== undefined ? rejects.find(option => option.scope === pill.scope) : undefined)
    ?? rejects.find(option => option.scope === undefined)
}

/**
 * The option a decision button sends.
 *
 * For Allow, the SELECTED scope pill when a scope group is drawn (falling back
 * to its first option when nothing valid is stored), else the once slot the
 * button displays. For Reject, a scope beyond Once upgrades to the agent's
 * reject_always of that scope when it offers one (see `rememberRejectFor`).
 *
 * An option the agent did not offer is never sent: the reject upgrade is
 * skipped when its variant is absent, and an unknown optionId is parsed as
 * cancel (goose) or reject (OpenCode) on the agent side.
 */
export function resolvePermissionOption(
  layout: PermissionOptionLayout,
  polarity: 'allow' | 'reject',
  selectedAllowScopeId?: string,
): PermissionOption | undefined {
  if (polarity === 'allow') {
    if (layout.allowScope) {
      const selected = layout.allowScope.find(option => option.optionId === selectedAllowScopeId)
      return selected ?? layout.allowScope[0]
    }
    return layout.positive
  }
  const pill = rememberingScope(layout, selectedAllowScopeId)
  const remember = pill && layout.rememberRejects ? rememberRejectFor(layout.rememberRejects, pill) : undefined
  return remember ?? layout.negative
}

/**
 * The label a decision button shows. A slot that holds a ONCE option carries the
 * polarity alone: a scope pill group, when drawn, states how long the answer
 * lasts. A slot that holds a REMEMBER option states its own duration through the
 * agent's own option name (`permissionOptionLabel`): the agent offered no once
 * variant, so the pills cannot qualify the answer. With a scope group drawn,
 * Allow always holds the once pill's option, but Deny holds the agent's
 * reject_always whatever pill is selected. A plain "Allow" or "Deny" there would
 * keep a permanent rule that the user cannot see.
 */
export function decisionLabel(layout: PermissionOptionLayout, polarity: 'allow' | 'reject'): string {
  const slot = polarity === 'allow' ? layout.positive : layout.negative
  const rememberKind = polarity === 'allow' ? KIND_ALLOW_ALWAYS : KIND_REJECT_ALWAYS
  return slot?.kind === rememberKind ? permissionOptionLabel(slot) : polarity === 'allow' ? 'Allow' : 'Deny'
}

/** The pill label of each scope that a plugin states. The widest scope reads Always. */
const SCOPE_LABELS: Record<PermissionScope, string> = {
  session: 'Session',
  workspace: 'Workspace',
  project: 'Project',
  user: 'Always',
}

/**
 * One scope pill's label.
 *
 * - The once slot reads Once.
 * - An option whose plugin states its scope reads the label of that scope.
 * - Every other always option reads its duration out of the agent's own option
 *   name ("...for this session", "Add to project allow_write"). The read looks
 *   only at the text before the first colon. An agent can put the text of the
 *   call after it (Qwen Code's "Always Allow for user: yarn workspace web
 *   build"), and a word of that text states no scope.
 * - A name that states no duration reads Always.
 *
 * Names are prose, so this is a keyword read, not a parse. A name with no
 * keyword before its colon gets a generic label, and a keyword in the words of
 * an agent's own label can still give the wrong one. A plugin that knows the
 * scope states it, so the label does not depend on the words.
 */
export function allowScopeLabel(option: PermissionOption): string {
  if (option.kind === KIND_ALLOW_ONCE)
    return 'Once'
  if (option.scope)
    return SCOPE_LABELS[option.scope]
  const label = (option.name ?? '').split(':', 1)[0]!.toLowerCase()
  if (label.includes('project'))
    return 'Project'
  if (label.includes('session'))
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
export function allowScopePillOptions(scope: readonly PermissionOption[]): PillOptions<string> | undefined {
  const labels = disambiguateLabels(scope, allowScopeLabel, permissionOptionLabel)
  const pills = scope.map((option, index) => ({ key: option.optionId, label: labels[index]! }))
  return isPillOptions(pills) ? pills : undefined
}
