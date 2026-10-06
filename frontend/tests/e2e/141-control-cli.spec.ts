/**
 * `leapmux control` end-to-end coverage for the single-worker case.
 *
 * The Go integration tests already prove every broadcast path the
 * spec touches at the protocol level (delegation tokens,
 * `UserCRDT /ws/userevents` fan-out, `SubmitOps` broadcast, channel
 * teardown). What they cannot exercise is the live wire-up: a real
 * frontend bundle running against a real hub, observing CLI-driven
 * mutations propagate into the DOM.
 *
 * The exercised signal is: `agent open` via the CLI → worker spawns
 * the agent → CRDT `SetTabRegister` batch submitted to the hub →
 * canonical-HLC-tagged op broadcast on `/ws/userevents` →
 * `useUserEvents` feeds it into `pendingOps.consumeRemote` → tab.store
 * projects the new tab → tab element appears in the DOM with the
 * agent's `tab_id` rendered as `data-tab-id`.
 *
 * Active-tab is now a purely local concern (sessionStorage); the
 * CRDT model has no `RequestActiveTab` broadcast, so the spec no
 * longer exercises remote focus propagation.
 *
 * The two-browser variant repeats the open path for the multi-tab
 * case (different sessions viewing the same workspace), proving the
 * op-stream fan-out reaches every subscriber, not just the originator.
 */

import { expect, test } from './fixtures'
import { cliAgentOpen, mintCLITokenForAdmin } from './helpers/cli'
import { withExtraClients } from './helpers/multiClient'
import { expectAgentTabCount, loginViaToken, openWorkspace, tabById } from './helpers/ui'

test.describe('control CLI live broadcast', () => {
  test('single browser observes CLI-driven agent open', async ({ page, authenticatedWorkspace, leapmuxServer }) => {
    const { workspaceId } = authenticatedWorkspace
    const cli = await mintCLITokenForAdmin(leapmuxServer)
    await expectAgentTabCount(page, 1)

    // Drive the CLI from outside the browser. The hub broadcasts a
    // canonical-HLC-tagged `CrdtOp` on `/ws/userevents` describing the
    // new tab; the live frontend's `useUserEvents` feeds it into
    // `pendingOps.consumeRemote` and the projection-driven
    // `tabStore` renders the new tab — that's the wire-up under
    // test here.
    const newAgentID = await cliAgentOpen(cli, { workspaceId, workerId: leapmuxServer.workerId })
    await expect(tabById(page, newAgentID)).toBeVisible()
    await expectAgentTabCount(page, 2)
  })

  test('two browsers viewing the same workspace both reflect the broadcast', async ({ browser, workspace, leapmuxServer }) => {
    const { workspaceId } = workspace
    const { adminToken, workerId } = leapmuxServer
    const cli = await mintCLITokenForAdmin(leapmuxServer)

    // Two browser contexts simulate a user logged in on two devices
    // (or two windows). Both should observe the snapshot fan-out.
    await withExtraClients(browser, leapmuxServer, 2, async ([pageA, pageB]) => {
      await loginViaToken(pageA, adminToken)
      await loginViaToken(pageB, adminToken)
      await Promise.all([
        openWorkspace(pageA, workspaceId),
        openWorkspace(pageB, workspaceId),
      ])
      await Promise.all([expectAgentTabCount(pageA, 1), expectAgentTabCount(pageB, 1)])

      // CLI creates a tab; both browsers must see it via snapshot
      // reconciliation. Active-tab is purely local under the CRDT
      // model (see `tab.go: "tab focus is intentionally absent"`), so
      // the spec asserts visibility only.
      const newAgentID = await cliAgentOpen(cli, { workspaceId, workerId })
      await Promise.all([
        expect(tabById(pageA, newAgentID)).toBeVisible(),
        expect(tabById(pageB, newAgentID)).toBeVisible(),
      ])
      await Promise.all([expectAgentTabCount(pageA, 2), expectAgentTabCount(pageB, 2)])
    })
  })
})
