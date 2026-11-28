/**
 * End-to-end coverage for tile, floating window, and layout CLI commands
 * against a live Hub, Worker, and browser UI:
 *
 *   tile split|close|make-grid|remove-grid|set-ratios|set-grid-ratios|resize|swap,
 *   window list|create|move|resize|set-opacity|close,
 *   layout get|set|watch.
 */

import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { test } from './fixtures'
import { mintCLITokenForAdmin, runCLI, spawnCLILive } from './helpers/cli'
import { createTestDirectory } from './helpers/runDirectory'
import { tabById } from './helpers/ui'

interface LayoutTreeNode {
  kind: string
  node_id?: string
  children?: LayoutTreeNode[]
  ratios?: number[]
  row_ratios?: number[]
  col_ratios?: number[]
}

interface LayoutGetResult {
  root_node_id: string
  tree: LayoutTreeNode
}

test.describe('control CLI tiles, floating windows, and layout', () => {
  test('splits, resizes, sets ratios, swaps, grids, and watches workspace layouts', async ({ page, authenticatedWorkspace, leapmuxServer }) => {
    const cli = await mintCLITokenForAdmin(leapmuxServer)
    const { workspaceId, agentId } = authenticatedWorkspace
    await expect(tabById(page, agentId)).toBeVisible()

    const watch = spawnCLILive(cli, ['layout', 'watch', workspaceId], { json: true })
    try {
      const readLayout = () => runCLI(cli, ['layout', 'get', workspaceId]) as Promise<LayoutGetResult>
      const initial = await readLayout()
      expect(initial.root_node_id).toBeTruthy()

      // Split root vertically, resize, set explicit ratios, swap children, then close the second tile.
      await runCLI(cli, [
        'tile',
        'split',
        initial.root_node_id,
        '--direction',
        'vertical',
      ])
      const afterSplit = await readLayout()
      expect(afterSplit.tree.kind).toBe('split')
      expect(afterSplit.tree.children).toHaveLength(2)
      const [leftTile, rightTile] = afterSplit.tree.children!
      expect(leftTile?.node_id).toBeTruthy()
      expect(rightTile?.node_id).toBeTruthy()

      await runCLI(cli, [
        'tile',
        'resize',
        leftTile!.node_id!,
        '--ratio',
        '0.4',
      ])
      await runCLI(cli, [
        'tile',
        'set-ratios',
        afterSplit.root_node_id,
        '--ratios',
        '0.35,0.65',
      ])
      const afterRatios = await readLayout()
      expect(afterRatios.tree.ratios?.[0]).toBeCloseTo(0.35, 2)
      expect(afterRatios.tree.ratios?.[1]).toBeCloseTo(0.65, 2)

      await runCLI(cli, [
        'tile',
        'swap',
        leftTile!.node_id!,
        rightTile!.node_id!,
      ])
      await runCLI(cli, [
        'tile',
        'close',
        rightTile!.node_id!,
        '--with-tabs',
        'move',
        '--allow-busy',
      ])

      // Convert root into a 2x2 grid, set grid ratios, then remove the grid.
      const single = await readLayout()
      await runCLI(cli, [
        'tile',
        'make-grid',
        single.root_node_id,
        '--rows',
        '2',
        '--cols',
        '2',
      ])
      const gridded = await readLayout()
      expect(gridded.tree.kind).toBe('grid')
      expect(gridded.tree.children).toHaveLength(4)

      await runCLI(cli, [
        'tile',
        'set-grid-ratios',
        gridded.root_node_id,
        '--row-ratios',
        '0.3,0.7',
        '--col-ratios',
        '0.4,0.6',
      ])
      const afterGridRatios = await readLayout()
      expect(afterGridRatios.tree.row_ratios?.[0]).toBeCloseTo(0.3, 2)
      expect(afterGridRatios.tree.col_ratios?.[0]).toBeCloseTo(0.4, 2)

      // Save and restore with `layout set`.
      const layoutFile = join(createTestDirectory('leapmux-layout-set-'), 'layout.json')
      writeFileSync(layoutFile, JSON.stringify(afterGridRatios))
      await runCLI(cli, [
        'layout',
        'set',
        workspaceId,
        layoutFile,
      ])

      await runCLI(cli, [
        'tile',
        'remove-grid',
        gridded.root_node_id,
        '--with-tabs',
        'move',
        '--allow-busy',
      ])
      const collapsed = await readLayout()
      expect(collapsed.tree.kind).toBe('leaf')

      // `layout watch` must have observed live layout snapshots.
      await expect.poll(() => watch.jsonLines().length).toBeGreaterThanOrEqual(2)
    }
    finally {
      await watch.stop()
    }
  })

  test('creates, lists, moves, resizes, adjusts opacity, and closes floating windows', async ({ page, authenticatedWorkspace, leapmuxServer }) => {
    const cli = await mintCLITokenForAdmin(leapmuxServer)
    const { workspaceId } = authenticatedWorkspace

    const created = await runCLI(cli, [
      'window',
      'create',
      workspaceId,
      '--x',
      '0.1',
      '--y',
      '0.1',
      '--width',
      '0.4',
      '--height',
      '0.45',
      '--opacity',
      '0.9',
    ]) as { id: string }
    expect(created.id).toBeTruthy()
    await expect(page.locator('[data-testid="floating-window"]')).toHaveCount(1)

    const listed = await runCLI(cli, ['window', 'list', workspaceId]) as {
      windows: Array<{ id: string, x?: number, opacity?: number }>
    }
    expect(listed.windows.some(w => w.id === created.id)).toBe(true)

    await runCLI(cli, [
      'window',
      'move',
      created.id,
      '--workspace',
      workspaceId,
      '--x',
      '0.2',
      '--y',
      '0.2',
    ])
    await runCLI(cli, [
      'window',
      'resize',
      created.id,
      '--width',
      '0.5',
      '--height',
      '0.5',
    ])
    await runCLI(cli, [
      'window',
      'set-opacity',
      created.id,
      '0.75',
    ])

    const updated = await runCLI(cli, ['window', 'list', workspaceId]) as {
      windows: Array<{ id: string, x?: number, width?: number, opacity?: number }>
    }
    const win = updated.windows.find(w => w.id === created.id)
    expect(win).toBeTruthy()
    expect(win?.x).toBeCloseTo(0.2, 2)
    expect(win?.width).toBeCloseTo(0.5, 2)
    expect(win?.opacity).toBeCloseTo(0.75, 2)

    await runCLI(cli, [
      'window',
      'close',
      created.id,
      '--allow-busy',
    ])
    await expect(page.locator('[data-testid="floating-window"]')).toHaveCount(0)
  })
})
