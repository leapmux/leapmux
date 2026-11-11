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
 * tab with the agent's `tab_id` as `data-tab-id`. Process startup can finish
 * after publication.
 *
 * Active-tab selection stays local in browser storage.
 *
 * The two-browser variant opens an agent while different sessions view the
 * same workspace. Both subscribers must render the published tab.
 */

import { expect } from '@playwright/test'
import { test } from './fixtures'
import { cliAgentOpen, mintCLITokenForAdmin } from './helpers/cli'
import { withExtraClients } from './helpers/multiClient'
import { expectAgentTabCount, loginViaToken, openWorkspace, tabById } from './helpers/ui'

test.describe('control CLI live broadcast', () => {
  test('single browser observes CLI-driven agent open', async ({ page, authenticatedWorkspace, leapmuxServer }) => {
    const { workspaceId } = authenticatedWorkspace
    const cli = await mintCLITokenForAdmin(leapmuxServer)
    await expectAgentTabCount(page, 1)

    // The CLI invokes Worker creation. Canonical-HLC-tagged CrdtOp batches
    // arrive on /ws/userevents. useUserEvents passes them to
    // pendingOps.consumeRemote, then the projection and joined view render the tab.
    const newAgentID = await cliAgentOpen(cli, { workspaceId, workerId: leapmuxServer.workerId })
    await expect(tabById(page, newAgentID)).toBeVisible()
    await expectAgentTabCount(page, 2)
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
