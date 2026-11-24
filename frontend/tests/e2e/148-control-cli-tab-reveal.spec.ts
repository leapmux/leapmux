/**
 * End-to-end coverage of `leapmux control tab reveal`.
 *
 * Go tests cover the request and the Worker's refusal of a tab that it does not
 * host. This spec runs the CLI against the real Hub and Worker, and observes
 * the browser switch to another workspace and activate the tab there, as a
 * click on the tab in the sidebar does.
 */

import { expect } from '@playwright/test'
import { test } from './fixtures'
import { mintCLITokenForAdmin, runCLI } from './helpers/cli'
import { activeWorkspaceId, sidebarLeafIds, tabById, workspaceRow } from './helpers/ui'
import { withTestWorkspace } from './helpers/workspace'

/** The result of `terminal open`. */
interface OpenedTerminal {
  id: string
}

test.describe('control CLI tab reveal', () => {
  test('brings a tab of another workspace into view, and back', async ({ page, authenticatedWorkspace, leapmuxServer }) => {
    const cli = await mintCLITokenForAdmin(leapmuxServer)
    const home = authenticatedWorkspace.workspaceId

    await withTestWorkspace(leapmuxServer, 'reveal', async ({ workspaceId: other }) => {
      const opened = await runCLI(cli, [
        'terminal',
        'open',
        '--workspace',
        other,
        '--worker',
        leapmuxServer.workerId,
      ]) as OpenedTerminal

      // A window ignores a reveal of a tab that it does not know yet, so the
      // tab must reach this window first. The window stays on its own
      // workspace meanwhile.
      await expect.poll(() => sidebarLeafIds(page, other), { message: 'the sidebar lists the new terminal' }).toContain(opened.id)
      expect(await activeWorkspaceId(page)).toBe(home)

      await runCLI(cli, ['tab', 'reveal', opened.id])

      await expect(workspaceRow(page, other)).toHaveAttribute('data-active', 'true')
      await expect(tabById(page, opened.id)).toBeVisible()
      await expect(tabById(page, opened.id)).toHaveAttribute('aria-selected', 'true')

      // And back: the agent tab of the first workspace becomes active again.
      await runCLI(cli, ['tab', 'reveal', authenticatedWorkspace.agentId])

      await expect(workspaceRow(page, home)).toHaveAttribute('data-active', 'true')
      await expect(tabById(page, authenticatedWorkspace.agentId)).toHaveAttribute('aria-selected', 'true')
    })
  })
})
