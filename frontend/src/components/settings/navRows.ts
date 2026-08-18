import type { NavGroup } from './navGroups'
import type { SettingRowModel } from './types'
import { descriptorVisible } from './types'

/**
 * Every visible row, in the navigation group that renders it.
 *
 * ONE statement of the membership rule, for every consumer: the panel, the
 * navigation's occupancy test, the restart badge and warning, and the
 * search index. Each of them re-derived it — pick the source from
 * `group.admin`, match the category, then read `hidden` — so the rule was
 * stated five times and could only be corrected in all five at once.
 *
 * A user group draws on the browser REGISTRY alone, and an admin group on
 * the hub's rows alone. A non-admin session gets no admin rows at all, so
 * an ADMINISTRATION group is empty rather than hidden by a second rule.
 *
 * Every group gets an entry, empty ones included, so a caller can address
 * a group it has not yet tested for occupancy.
 */
export function groupRowsByNav(
  groups: readonly NavGroup[],
  args: {
    isAdmin: boolean
    browserRows: readonly SettingRowModel[]
    adminRows: readonly SettingRowModel[]
  },
): Map<string, SettingRowModel[]> {
  const byNav = new Map<string, SettingRowModel[]>()
  for (const group of groups) {
    const source = group.admin
      ? (args.isAdmin ? args.adminRows : [])
      : args.browserRows
    byNav.set(group.id, source.filter(row =>
      row.descriptor.category === group.category && descriptorVisible(row.descriptor)))
  }
  return byNav
}

/**
 * Navigation groups that currently have at least one visible row.
 *
 * Admin groups whose every descriptor is hidden (captcha and rate-limits
 * in solo mode) drop out instead of rendering an empty panel. User groups
 * whose only rows are hidden (Account in solo) drop out the same way, and
 * so does every admin group of a non-admin session.
 */
export function occupiedNavGroups(
  groups: readonly NavGroup[],
  rowsByNav: ReadonlyMap<string, readonly SettingRowModel[]>,
): NavGroup[] {
  return groups.filter(g => (rowsByNav.get(g.id)?.length ?? 0) > 0)
}
