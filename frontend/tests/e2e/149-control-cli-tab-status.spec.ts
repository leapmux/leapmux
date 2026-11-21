/**
 * End-to-end coverage of `leapmux control tab status`.
 *
 * Go tests cover the request, the validation and the Worker's store. This spec
 * runs the CLI against the real Hub and Worker, and observes the status beside
 * the tab's title in the tab bar and in the sidebar, across a reload, which
 * the Worker answers from its bootstrap replay, and after a clear.
 */

import type { Page } from '@playwright/test'
import { expect } from '@playwright/test'
import { test } from './fixtures'
import { mintCLITokenForAdmin, runCLI } from './helpers/cli'
import { tabIdSelector } from './helpers/tabSelectors'
import { tabById, waitForWorkspaceReady, workspaceChildren } from './helpers/ui'

/** The sidebar row of one tab, below the visible row of its workspace. */
function sidebarLeaf(page: Page, workspaceId: string, tabId: string) {
  return workspaceChildren(page, workspaceId).locator(`[data-testid="tab-tree-leaf"]${tabIdSelector(tabId)}`)
}

test.describe('control CLI tab status', () => {
  test('shows the status and its progress on the tab, and clear removes it', async ({ page, authenticatedWorkspace, leapmuxServer }) => {
    const cli = await mintCLITokenForAdmin(leapmuxServer)
    const { workspaceId, agentId } = authenticatedWorkspace
    const tab = tabById(page, agentId)
    const leaf = sidebarLeaf(page, workspaceId, agentId)
    await expect(tab).toBeVisible()

    await runCLI(cli, [
      'tab',
      'status',
      'set',
      agentId,
      'Running the tests',
      '--progress',
      '40',
      '--tone',
      'warning',
    ])

    for (const row of [tab, leaf]) {
      const status = row.getByTestId('tab-status')
      await expect(status.getByTestId('tab-status-text')).toHaveText('Running the tests')
      await expect(status).toHaveAttribute('data-tone', 'warning')
      await expect(status.getByRole('progressbar')).toHaveAttribute('aria-valuenow', '40')
    }

    // A window that opens later shows the status too: the Worker replays each
    // status that is set when the window subscribes.
    await page.reload()
    await waitForWorkspaceReady(page)
    await expect(tab.getByTestId('tab-status-text')).toHaveText('Running the tests')

    // A new status replaces the old one whole: no progress, another tone.
    await runCLI(cli, [
      'tab',
      'status',
      'set',
      agentId,
      'Tests passed',
      '--tone',
      'success',
    ])
    await expect(tab.getByTestId('tab-status-text')).toHaveText('Tests passed')
    await expect(tab.getByTestId('tab-status')).toHaveAttribute('data-tone', 'success')
    await expect(tab.getByRole('progressbar')).toHaveCount(0)

    await runCLI(cli, ['tab', 'status', 'clear', agentId])
    await expect(tab.getByTestId('tab-status')).toHaveCount(0)
    await expect(leaf.getByTestId('tab-status')).toHaveCount(0)
  })

  test('keeps independent status keys through reconnect and clear', async ({ page, authenticatedWorkspace, leapmuxServer }) => {
    const cli = await mintCLITokenForAdmin(leapmuxServer)
    const { workspaceId, agentId } = authenticatedWorkspace
    const tab = tabById(page, agentId)
    const leaf = sidebarLeaf(page, workspaceId, agentId)
    await expect(tab).toBeVisible()

    const set = async (key: string, text: string) => runCLI(cli, [
      'tab',
      'status',
      'set',
      agentId,
      text,
      '--key',
      key,
    ])
    const clear = async (key: string) => runCLI(cli, [
      'tab',
      'status',
      'clear',
      agentId,
      '--key',
      key,
    ])
    const list = async () => runCLI(cli, [
      'tab',
      'status',
      'list',
      agentId,
    ])

    await set('build', 'Compiling the project')
    await set('test', 'Running browser tests')
    expect(await list()).toEqual({
      id: agentId,
      statuses: [
        { key: 'test', text: 'Running browser tests', tone: 'neutral' },
        { key: 'build', text: 'Compiling the project', tone: 'neutral' },
      ],
    })
    await page.reload()
    await waitForWorkspaceReady(page)
    await expect(tab.getByTestId('tab-status-text')).toHaveText('Running browser tests')

    await clear('test')
    for (const row of [tab, leaf])
      await expect(row.getByTestId('tab-status-text')).toHaveText('Compiling the project')
    expect(await list()).toEqual({
      id: agentId,
      statuses: [
        { key: 'build', text: 'Compiling the project', tone: 'neutral' },
      ],
    })
    await clear('build')
    await expect(tab.getByTestId('tab-status')).toHaveCount(0)
    expect(await list()).toEqual({ id: agentId, statuses: [] })
  })
})
