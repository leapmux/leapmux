/**
 * The default share of a sidebar that each expanded section takes.
 *
 * A section that declares a `defaultSize` reserves that fraction, and the
 * sections that declare none split what is left. That rule alone breaks once
 * the declarations claim the whole sidebar: an undeclared section then computes
 * a zero share -- or a negative one past 1.0 -- and renders with no height at
 * all, header included. Every section is draggable between the two sidebars, so
 * one drop puts a workspace section beside the three right-sidebar sections
 * whose declared defaults already sum to exactly 1.0.
 *
 * When the declarations leave nothing, every section takes an equal weight
 * instead. The result is normalized either way, so no section can reach zero.
 *
 * Pure and exported so both callers -- the visibility effect in
 * `./useSectionToggle` and the double-click reset in `./useResizeHandle` --
 * derive the same answer. Two spellings of "the default split" is how a reset
 * and a redistribute come to disagree.
 */
export function distributeSectionSizes(
  ids: readonly string[],
  declared: ReadonlyMap<string, number>,
): Map<string, number> {
  const sizes = new Map<string, number>()
  if (ids.length === 0)
    return sizes

  let declaredTotal = 0
  let undeclaredCount = 0
  for (const id of ids) {
    const size = declared.get(id)
    if (size === undefined)
      undeclaredCount++
    else
      declaredTotal += size
  }

  const remaining = 1 - declaredTotal
  const undeclaredShare = undeclaredCount > 0 ? remaining / undeclaredCount : 0
  // The declarations over-subscribe the sidebar, so they cannot also fund the
  // sections that declare nothing. Weigh every section alike and let the
  // normalize below share the space out.
  const equalWeights = undeclaredCount > 0 && undeclaredShare <= 0

  let total = 0
  for (const id of ids) {
    const weight = equalWeights ? 1 : (declared.get(id) ?? undeclaredShare)
    sizes.set(id, weight)
    total += weight
  }
  if (total <= 0) {
    for (const id of ids)
      sizes.set(id, 1 / ids.length)
    return sizes
  }
  for (const [id, weight] of sizes)
    sizes.set(id, weight / total)
  return sizes
}
