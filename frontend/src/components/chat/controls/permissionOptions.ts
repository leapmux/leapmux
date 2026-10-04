import type { PermissionOption, PermissionScope } from '~/components/chat/model/controlPrompt'
import type { PillOptions } from '~/components/common/pillOptions'
import { CANONICAL_KINDS, KIND_ALLOW_ALWAYS, KIND_ALLOW_ONCE, KIND_REJECT_ALWAYS, KIND_REJECT_ONCE } from '~/components/chat/model/controlPrompt'
import { disambiguateLabels, isPillOptions, PILL_OPTION_LIMIT } from '~/components/common/pillOptions'
import { permissionOptionLabel } from './permissionOptionLabels'

/**
 * Arrange a permission request's options by their canonical kinds.
 * Native optionId values differ between providers, so their spelling cannot determine the kind.
 *
 * - Decision buttons show Allow or Deny. Scope choices show the duration of a once answer.
 * - A single allow_once and available allow_always options form allowScope when the list fits the pill limit.
 *   Put Once first and keep the native payload order for the remaining scopes.
 * - A selected remembered scope can select its matching reject_always answer when the provider offers one.
 * - The first option of each canonical kind fills its slot.
 *   Keep every unused option in additional, including unknown kinds and duplicates, so no offered answer disappears.
 *
 * A wider scope list uses separate additional buttons instead of a scope group.
 * The layout puts negative actions first without changing the optionId that the native reply carries.
 */
export interface PermissionOptionLayout {
  negative?: PermissionOption
  /** The option Allow sends while no scope control overrides it (the once slot, or the only allow). */
  positive?: PermissionOption
  /**
   * These remembered refusal options are reachable through a displayed scope group beside reject_once.
   * Omit this field when no scope choice can select a reject_always answer.
   */
  rememberRejects?: PermissionOption[]
  /**
   * The scope group contains one allow_once followed by the available allow_always options in payload order.
   * Omit it without a single once choice, without a remembered scope, or when the list exceeds the pill limit.
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

  // Require one allow_once choice and at least one remembered scope.
  // Two once choices are alternative replies and cannot define one Once duration.
  // Require the group to fit the pill limit.
  // Keep wider choices available as additional buttons.
  // Require distinct option IDs because the native reply carries that ID.
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

  // Remove an option from additional only when a rendered slot or scope group uses it.
  // A displayed scope group uses its own options and the refusal choices that its scopes can select.
  // Without a scope group, keep each remembered refusal available as an additional button.
  // A refusal that no scope can select also stays additional.
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

  // Set an optional slot only when it contains an option.
  // The caller uses an absent field for an unavailable choice.
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
 * Collect reachable remembered refusal options in payload order.
 *
 * - A refusal without a stated scope can answer each remembered scope.
 * - A refusal with a matching scope answers that scope.
 * - Keep the first refusal for each scope and the first refusal without a scope.
 *
 * Other refusal options remain available as additional buttons.
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
 * Read the selected remembered scope beyond Once.
 * Use it to select the matching remembered refusal through the same scope control.
 */
function rememberingScope(
  layout: PermissionOptionLayout,
  selectedAllowScopeId?: string,
): PermissionOption | undefined {
  const selected = layout.allowScope?.find(option => option.optionId === selectedAllowScopeId)
  return selected?.kind === KIND_ALLOW_ALWAYS ? selected : undefined
}

/**
 * Select the remembered refusal for one remembered scope.
 * Use an exact scope match first, then a refusal that states no scope.
 * Never select a refusal for a different scope.
 * If neither matches, the decision button keeps its once refusal.
 */
function rememberRejectFor(rejects: readonly PermissionOption[], pill: PermissionOption): PermissionOption | undefined {
  return (pill.scope !== undefined ? rejects.find(option => option.scope === pill.scope) : undefined)
    ?? rejects.find(option => option.scope === undefined)
}

/**
 * Select the native option for the requested decision.
 * Allow uses the selected scope, or the first scope if the stored selection is invalid.
 * Without a scope group, Allow uses its positive slot.
 * Reject uses a remembered refusal only when the selected scope can reach that offered answer.
 * Otherwise, Reject uses its negative slot.
 * Never send an option that the provider did not offer.
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
 * A once decision uses the plain Allow or Deny label.
 * The scope group supplies its duration when present.
 * A remembered slot uses the native option's duration label because it can store a rule.
 * That label must remain visible even when the group selects Once.
 * A plain label on a remembered slot would conceal the lasting rule.
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
 * Read the scope label from the option.
 *
 * - allow_once reads Once.
 * - An explicit plugin scope uses its declared scope label.
 * - Otherwise, inspect only the native name before its first colon for a project or session keyword.
 * - Without a duration keyword, use Always.
 *
 * The text after the colon can contain the command and must not establish duration.
 * A native prose label can still use an ambiguous keyword.
 * An explicit plugin scope avoids that ambiguity.
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
 * Use each native optionId as its scope selection key.
 * Return undefined when the group falls outside the pill count limit.
 * The caller then uses the ordinary Allow button.
 * When scope labels collide, use the distinct native option labels so the user can tell the choices apart.
 */
export function allowScopePillOptions(scope: readonly PermissionOption[]): PillOptions<string> | undefined {
  const labels = disambiguateLabels(scope, allowScopeLabel, permissionOptionLabel)
  const pills = scope.map((option, index) => ({ key: option.optionId, label: labels[index]! }))
  return isPillOptions(pills) ? pills : undefined
}
