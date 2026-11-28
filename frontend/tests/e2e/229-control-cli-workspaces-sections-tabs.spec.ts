/**
 * End-to-end coverage for tree, section, workspace, tab, notify, and
 * notification CLI commands against a live Hub, Worker, and browser UI:
 *
 *   tree,
 *   section list|create|rename|move|delete,
 *   workspace list|get|create|rename|archive|unarchive|move|delete|export|apply,
 *   tab list|get|close|rename|move|reveal|status set|status list|status clear,
 *   notify, notification send|list|clear.
 */

import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { test } from './fixtures'
import { mintCLITokenForAdmin, runCLI } from './helpers/cli'
import { createTestDirectory } from './helpers/runDirectory'
import { expectToastRecorded } from './helpers/toast'
import { tabById, workspaceRow, workspaceRowTitle } from './helpers/ui'

test.describe('control CLI workspaces, sections, tabs, and notifications', () => {
  test('manages sections and workspaces through full lifecycle including export and apply', async ({ page, authenticatedWorkspace, leapmuxServer }) => {
    const cli = await mintCLITokenForAdmin(leapmuxServer)

    // --- Sections: list, create, rename, move, delete ---
    const initialSections = await runCLI(cli, ['section', 'list']) as {
      sections: Array<{ id: string, title?: string, name?: string }>
    }
    expect(initialSections.sections.length).toBeGreaterThan(0)

    const sec1 = await runCLI(cli, ['section', 'create', 'CLI Review']) as { id: string }
    const sec2 = await runCLI(cli, ['section', 'create', 'CLI Staging']) as { id: string }
    expect(sec1.id).toBeTruthy()
    expect(sec2.id).toBeTruthy()

    await runCLI(cli, [
      'section',
      'rename',
      sec1.id,
      'CLI Renamed',
    ])
    await runCLI(cli, [
      'section',
      'move',
      sec2.id,
      '--before',
      sec1.id,
    ])

    const afterSecMove = await runCLI(cli, ['section', 'list']) as {
      sections: Array<{ id: string, title?: string, name?: string }>
    }
    const idx2 = afterSecMove.sections.findIndex(s => s.id === sec2.id)
    const idx1 = afterSecMove.sections.findIndex(s => s.id === sec1.id)
    expect(idx2).toBeGreaterThanOrEqual(0)
    expect(idx1).toBeGreaterThan(idx2)

    // --- Workspaces: create, list, get, rename, move, archive, unarchive ---
    const ws = await runCLI(cli, ['workspace', 'create', 'CLI Lifecycle WS']) as { id: string }
    expect(ws.id).toBeTruthy()
    await expect(workspaceRow(page, ws.id)).toBeVisible()

    const listed = await runCLI(cli, ['workspace', 'list']) as {
      workspaces: Array<{ id: string, title: string }>
    }
    expect(listed.workspaces.some(w => w.id === ws.id)).toBe(true)

    const got = await runCLI(cli, ['workspace', 'get', ws.id]) as { id: string, title: string }
    expect(got.id).toBe(ws.id)

    await runCLI(cli, [
      'workspace',
      'rename',
      ws.id,
      'CLI Lifecycle Renamed',
    ])
    await expect(workspaceRowTitle(page, ws.id)).toHaveText('CLI Lifecycle Renamed')

    await runCLI(cli, [
      'workspace',
      'move',
      ws.id,
      '--section',
      sec1.id,
      '--first',
    ])
    await runCLI(cli, ['workspace', 'archive', ws.id])
    const activeOnly = await runCLI(cli, ['workspace', 'list']) as { workspaces: Array<{ id: string }> }
    expect(activeOnly.workspaces.some(w => w.id === ws.id)).toBe(false)
    const withArchived = await runCLI(cli, ['workspace', 'list', '--archived']) as { workspaces: Array<{ id: string }> }
    expect(withArchived.workspaces.some(w => w.id === ws.id)).toBe(true)

    await runCLI(cli, [
      'workspace',
      'unarchive',
      ws.id,
      '--section',
      sec1.id,
    ])
    const unarchived = await runCLI(cli, ['workspace', 'list']) as { workspaces: Array<{ id: string }> }
    expect(unarchived.workspaces.some(w => w.id === ws.id)).toBe(true)

    // --- Export and apply workspace template ---
    const exportDir = createTestDirectory('leapmux-ws-export-')
    const exportFile = join(exportDir, 'workspace.json')
    await runCLI(cli, [
      'workspace',
      'export',
      ws.id,
      '--output',
      exportFile,
    ])
    expect(existsSync(exportFile)).toBe(true)
    expect(readFileSync(exportFile, 'utf8')).toContain('CLI Lifecycle Renamed')

    const dryRun = await runCLI(cli, [
      'workspace',
      'apply',
      exportFile,
      '--title',
      'CLI Applied Dry',
      '--worker',
      leapmuxServer.workerId,
      '--dry-run',
    ])
    expect(dryRun).toBeTruthy()

    const applied = await runCLI(cli, [
      'workspace',
      'apply',
      exportFile,
      '--title',
      'CLI Applied Real',
      '--worker',
      leapmuxServer.workerId,
    ]) as { workspace_id?: string, id?: string }
    const appliedId = applied.workspace_id ?? applied.id
    expect(appliedId).toBeTruthy()
    await expect(workspaceRow(page, appliedId!)).toBeVisible()

    // --- Tree command (hierarchical + flat) ---
    const tree = await runCLI(cli, ['tree', '--workspace', authenticatedWorkspace.workspaceId])
    expect(tree).toBeTruthy()
    const flatTree = await runCLI(cli, ['tree', '--flat', '--archived']) as {
      tabs?: Array<{ id: string }>
    }
    expect(Array.isArray(flatTree.tabs)).toBe(true)

    // Clean up created workspaces and sections.
    await runCLI(cli, [
      'workspace',
      'delete',
      appliedId!,
      '--allow-busy',
    ])
    await runCLI(cli, [
      'workspace',
      'delete',
      ws.id,
      '--allow-busy',
    ])
    await runCLI(cli, ['section', 'delete', sec1.id])
    await runCLI(cli, ['section', 'delete', sec2.id])
  })

  test('lists, inspects, renames, moves, reveals, sets status, and closes tabs plus sends notifications', async ({ page, authenticatedWorkspace, leapmuxServer }) => {
    const cli = await mintCLITokenForAdmin(leapmuxServer)
    const { workspaceId, agentId } = authenticatedWorkspace

    const t1 = await runCLI(cli, [
      'terminal',
      'open',
      '--workspace',
      workspaceId,
      '--worker',
      leapmuxServer.workerId,
      '--title',
      'Tab Alpha',
    ]) as { id: string, tile_id: string }
    const t2 = await runCLI(cli, [
      'terminal',
      'open',
      '--workspace',
      workspaceId,
      '--worker',
      leapmuxServer.workerId,
      '--title',
      'Tab Beta',
    ]) as { id: string, tile_id: string }

    await expect(tabById(page, t1.id)).toBeVisible()
    await expect(tabById(page, t2.id)).toBeVisible()

    const tabs = await runCLI(cli, [
      'tab',
      'list',
      '--workspace',
      workspaceId,
    ]) as {
      tabs: Array<{ id: string, title?: string }>
    }
    expect(tabs.tabs.map(t => t.id)).toEqual(expect.arrayContaining([agentId, t1.id, t2.id]))

    const gotTab = await runCLI(cli, ['tab', 'get', t1.id]) as { id: string, title?: string }
    expect(gotTab.id).toBe(t1.id)

    await runCLI(cli, [
      'tab',
      'rename',
      t1.id,
      'Tab Alpha Renamed',
    ])
    await expect(tabById(page, t1.id)).toContainText('Tab Alpha Renamed')

    await runCLI(cli, [
      'tab',
      'move',
      t2.id,
      '--before',
      t1.id,
    ])
    await runCLI(cli, ['tab', 'reveal', t1.id])
    await expect(tabById(page, t1.id)).toHaveAttribute('aria-selected', 'true')

    await runCLI(cli, [
      'tab',
      'status',
      'set',
      t1.id,
      'Building',
      '--key',
      'ci',
      '--tone',
      'warning',
      '--progress',
      '50',
    ])
    const statuses = await runCLI(cli, ['tab', 'status', 'list', t1.id]) as {
      statuses: Array<{ key: string, text: string }>
    }
    expect(statuses.statuses).toContainEqual(expect.objectContaining({ key: 'ci', text: 'Building' }))
    await runCLI(cli, [
      'tab',
      'status',
      'clear',
      t1.id,
      '--all',
    ])
    const cleared = await runCLI(cli, ['tab', 'status', 'list', t1.id]) as { statuses: unknown[] }
    expect(cleared.statuses).toEqual([])

    // --- Notifications: notify, notification send|list|clear ---
    const n1 = await runCLI(cli, [
      'notify',
      'CLI Notify Smoke',
      '--body',
      'Top-level notify alias',
      '--worker',
      leapmuxServer.workerId,
    ]) as { id: string }
    await expectToastRecorded(page, 'CLI Notify Smoke')

    const n2 = await runCLI(cli, [
      'notification',
      'send',
      'CLI Notification Send',
      '--body',
      'Full notification subcommand',
      '--worker',
      leapmuxServer.workerId,
    ]) as { id: string }
    await expectToastRecorded(page, 'CLI Notification Send')

    const listedNotifs = await runCLI(cli, [
      'notification',
      'list',
      '--worker',
      leapmuxServer.workerId,
    ]) as { notifications: Array<{ id: string }> }
    expect(listedNotifs.notifications.map(n => n.id)).toEqual(expect.arrayContaining([n1.id, n2.id]))

    await runCLI(cli, [
      'notification',
      'clear',
      n1.id,
      n2.id,
      '--worker',
      leapmuxServer.workerId,
    ])
    const afterClear = await runCLI(cli, [
      'notification',
      'list',
      '--worker',
      leapmuxServer.workerId,
    ]) as { notifications: Array<{ id: string }> }
    expect(afterClear.notifications.some(n => n.id === n1.id || n.id === n2.id)).toBe(false)

    // Close both opened terminal tabs via `tab close`.
    await runCLI(cli, [
      'tab',
      'close',
      t1.id,
      t2.id,
      '--allow-busy',
      '--worktree',
      'keep',
    ])
    await expect(tabById(page, t1.id)).toHaveCount(0)
    await expect(tabById(page, t2.id)).toHaveCount(0)
  })
})
