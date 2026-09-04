import type { RepoStartPoint } from './repoStartPoints'
import type { MenuInfoRow } from '~/components/common/MenuInfoRows'
import { prettifyJson } from '~/lib/jsonFormat'

/** Everything the workspace row menu's info block reports. */
export interface WorkspaceMenuInfo {
  workspaceId: string
  title: string
  sectionName: string
  tabCount: number
  repos: readonly RepoStartPoint[]
}

/**
 * The rows the info block shows.
 *
 * The repository row appears only when it is UNAMBIGUOUS. Two repositories
 * cannot share one row, and the `Repository` submenu lists them all anyway.
 */
export function workspaceInfoRows(info: WorkspaceMenuInfo): MenuInfoRow[] {
  const rows: MenuInfoRow[] = [
    { label: 'Workspace:', value: info.title },
    { label: 'Section:', value: info.sectionName },
    { label: 'Tabs:', value: String(info.tabCount) },
  ]
  if (info.repos.length === 1)
    rows.push({ label: 'Repository:', value: info.repos[0].label })
  return rows
}

/**
 * The same information as JSON, for the copy button.
 *
 * It carries the workspace ID, which no row shows and which the CLI takes.
 */
export function workspaceInfoJson(info: WorkspaceMenuInfo): string {
  return prettifyJson({
    workspaceId: info.workspaceId,
    title: info.title,
    section: info.sectionName,
    tabs: info.tabCount,
    repositories: info.repos.map(r => ({
      label: r.label,
      workerId: r.startPoint.workerId,
      path: r.startPoint.gitToplevel,
    })),
  })
}
