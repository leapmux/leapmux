import type { RepoStartPoint } from './repoStartPoints'
import { describe, expect, it } from 'vitest'
import { workspaceInfoJson, workspaceInfoRows } from './workspaceMenuInfo'

/**
 * The info projection is pure, so it is tested without mounting a menu -- which
 * is the point of extracting it from `WorkspaceContextMenu`.
 */
function repo(label: string, workerId = 'w1', gitToplevel = '/home/me/repo'): RepoStartPoint {
  return { label, startPoint: { kind: 'repo', workerId, gitToplevel, isWorktree: false } }
}

const base = {
  workspaceId: 'ws-1',
  title: 'My workspace',
  sectionName: 'In progress',
  tabCount: 3,
  repos: [] as RepoStartPoint[],
}

describe('workspaceInfoRows', () => {
  it('reports the workspace, its section and its tab count', () => {
    expect(workspaceInfoRows(base)).toEqual([
      { label: 'Workspace:', value: 'My workspace' },
      { label: 'Section:', value: 'In progress' },
      { label: 'Tabs:', value: '3' },
    ])
  })

  it('adds the repository row only when there is exactly one', () => {
    const one = workspaceInfoRows({ ...base, repos: [repo('leapmux')] })
    expect(one.at(-1)).toEqual({ label: 'Repository:', value: 'leapmux' })
  })

  // Two repositories cannot share one row, and the Repository submenu lists
  // them all anyway.
  it('omits the repository row for two or more', () => {
    const two = workspaceInfoRows({ ...base, repos: [repo('alpha'), repo('beta', 'w2')] })
    expect(two.map(r => r.label)).not.toContain('Repository:')
  })

  it('reports a zero tab count rather than dropping the row', () => {
    expect(workspaceInfoRows({ ...base, tabCount: 0 }).at(-1)).toEqual({ label: 'Tabs:', value: '0' })
  })
})

describe('workspaceInfoJson', () => {
  // The id is the whole reason the copy exists: no row shows it, and the CLI
  // takes it.
  it('carries the workspace id, which no row shows', () => {
    const parsed = JSON.parse(workspaceInfoJson(base))
    expect(parsed.workspaceId).toBe('ws-1')
  })

  it('lists every repository with its worker and path, however many there are', () => {
    const parsed = JSON.parse(workspaceInfoJson({
      ...base,
      repos: [repo('alpha', 'w1', '/a'), repo('beta', 'w2', '/b')],
    }))
    expect(parsed.repositories).toEqual([
      { label: 'alpha', workerId: 'w1', path: '/a' },
      { label: 'beta', workerId: 'w2', path: '/b' },
    ])
  })

  it('answers valid JSON for a workspace with no repository', () => {
    expect(JSON.parse(workspaceInfoJson(base)).repositories).toEqual([])
  })
})
