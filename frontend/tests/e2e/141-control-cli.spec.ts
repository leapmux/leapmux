/**
 * End-to-end coverage for `leapmux control` with one Worker.
 *
 * Go integration tests cover delegation tokens, canonical event delivery, and
 * channel teardown. This spec exercises the real frontend bundle against a
 * real Hub and observes CLI-driven creation in the DOM.
 *
 * CLI agent creation reaches the authenticated Worker. The Worker publishes
 * the canonical tab through the Hub, which broadcasts its operations on
 * `/ws/userevents`. The pending manager and projection produce the rendered
 * tab with the agent's id as `data-tab-id`. Process startup can finish
 * after publication.
 *
 * Active-tab selection stays local in browser storage.
 *
 * The two-browser variant opens an agent while different sessions view the
 * same workspace. Both subscribers must render the published tab.
 */

import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { test } from './fixtures'
import { cliAgentOpen, mintCLITokenForAdmin, runCLI } from './helpers/cli'
import { withExtraClients } from './helpers/multiClient'
import { createTestDirectory } from './helpers/runDirectory'
import { expectAgentTabCount, loginViaToken, openTerminalViaUI, openWorkspace, tabById } from './helpers/ui'

test.describe('control CLI live broadcast', () => {
  test('uses the same generated ID format for browser and CLI creation', async ({ page, authenticatedWorkspace, leapmuxServer }) => {
    const { workspaceId } = authenticatedWorkspace
    const cli = await mintCLITokenForAdmin(leapmuxServer)
    await expectAgentTabCount(page, 1)

    const terminalID = await openTerminalViaUI(page)
    expect(terminalID).toMatch(/^[a-z0-9]{48}$/i)

    // The CLI invokes Worker creation. Canonical-HLC-tagged CrdtOp batches
    // arrive on /ws/userevents. useUserEvents passes them to
    // pendingOps.consumeRemote, then the projection and joined view render the tab.
    const newAgentID = await cliAgentOpen(cli, { workspaceId, workerId: leapmuxServer.workerId })
    expect(newAgentID).toMatch(/^[a-z0-9]{48}$/i)
    await expect(tabById(page, newAgentID)).toBeVisible()
    await expectAgentTabCount(page, 2)
  })

  test('restores a saved grid without moving its split to another cell', async ({ page, authenticatedWorkspace, leapmuxServer }) => {
    const { workspaceId, agentId } = authenticatedWorkspace
    const cli = await mintCLITokenForAdmin(leapmuxServer)
    const readLayout = () => runCLI(cli, ['layout', 'get', workspaceId]) as Promise<{
      root_node_id: string
      tree: {
        kind: string
        children?: Array<{ kind: string, node_id?: string, direction?: string }>
        row_ratios?: number[]
        col_ratios?: number[]
      }
    }>
    const { root_node_id: rootID } = await readLayout()
    await runCLI(cli, [
      'tile',
      'make-grid',
      rootID,
      '--rows',
      '2',
      '--cols',
      '2',
    ])
    const grid = await readLayout()
    expect(grid.tree.kind).toBe('grid')
    expect(grid.tree.children).toHaveLength(4)
    const thirdCell = grid.tree.children?.[2]
    if (!thirdCell?.node_id)
      throw new Error('The exported grid has no addressable third cell.')
    await runCLI(cli, [
      'tile',
      'split',
      thirdCell.node_id,
      '--direction',
      'horizontal',
    ])

    const saved = await readLayout()
    const file = join(createTestDirectory('cli-layout-round-trip-'), 'grid.json')
    const restore = (layout: unknown) => {
      writeFileSync(file, JSON.stringify(layout))
      return runCLI(cli, [
        'layout',
        'set',
        workspaceId,
        file,
      ])
    }
    await restore(saved)

    const restored = await readLayout()
    expect(restored.root_node_id).toBe(rootID)
    expect(restored.tree.children?.map(cell => cell.kind)).toEqual(['leaf', 'leaf', 'split', 'leaf'])
    expect(restored.tree.children?.[2]?.direction).toBe('horizontal')
    await expect(page.getByTestId('tile-grid').getByTestId('tile-split')).toHaveCount(1)
    await expect(tabById(page, agentId)).toBeVisible()

    await restore({
      kind: 'grid',
      rows: 1,
      cols: 3,
      children: Array.from({ length: 3 }, () => ({ kind: 'leaf' })),
    })
    const reshaped = await readLayout()
    expect(reshaped.root_node_id).toBe(rootID)
    expect(reshaped.tree.children).toHaveLength(3)
    expect(reshaped.tree.row_ratios).toEqual([1])
    expect(reshaped.tree.col_ratios).toEqual([1 / 3, 1 / 3, 1 / 3])
    await restore(reshaped)
    await expect(page.getByTestId('tile-grid').getByTestId('tile-split')).toHaveCount(0)
    await expect(tabById(page, agentId)).toBeVisible()
  })

  test('two browsers viewing the same workspace both reflect the broadcast', async ({ browser, workspace, leapmuxServer }) => {
    const { workspaceId } = workspace
    const { adminToken, workerId } = leapmuxServer
    const cli = await mintCLITokenForAdmin(leapmuxServer)

    // Two browser contexts simulate a user logged in on two devices
    // or two windows. Both should receive the canonical operations.
    await withExtraClients(browser, leapmuxServer, 2, async ([pageA, pageB]) => {
      await loginViaToken(pageA, adminToken)
      await loginViaToken(pageB, adminToken)
      await Promise.all([
        openWorkspace(pageA, workspaceId),
        openWorkspace(pageB, workspaceId),
      ])
      await Promise.all([
        expectAgentTabCount(pageA, 1),
        expectAgentTabCount(pageB, 1),
      ])

      // Both browsers must render the CLI-created tab from canonical events.
      // Each browser owns its active selection, so these assertions check visibility.
      const newAgentID = await cliAgentOpen(cli, { workspaceId, workerId })
      await Promise.all([
        expect(tabById(pageA, newAgentID)).toBeVisible(),
        expect(tabById(pageB, newAgentID)).toBeVisible(),
      ])
      await Promise.all([
        expectAgentTabCount(pageA, 2),
        expectAgentTabCount(pageB, 2),
      ])
    })
  })
})
