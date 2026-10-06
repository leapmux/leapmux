import { expect } from '@playwright/test'
import { test } from './fixtures'
import { createWorkspaceViaAPI } from './helpers/api'
import { withExtraClients } from './helpers/multiClient'
import { gotoWorkspace, tiles, waitForLayoutSave, workspaceRow } from './helpers/ui'

/**
 * Cross-workspace tab move convergence and workspace isolation.
 *
 * The plan's invariant: a cross-workspace tab move is one CRDT op
 * batch (`SetTabRegister(tile_id=newTileInW2)` + position). The hub
 * resolves the new owning workspace via the new tile's ancestor
 * chain. Source-only subscribers see `EntityRemoved`; destination-
 * only subscribers see `EntityMaterialized`. Both views update
 * without flickering through a "no workspace" intermediate state.
 *
 * The sidebar drag-to-workspace gesture's CRDT contract — a single
 * `SetTabRegister(tile_id=newTileInW2)` + `SetTabRegister(position)`
 * batch instead of tombstone-then-re-add — is exercised at the unit
 * level in `src/stores/tab.store.crdt.test.ts`
 * (`moveTabToWorkspace emits a single batch with tile_id + position`).
 * The full UI gesture E2E currently depends on per-workspace worker-
 * provider availability that the dev fixture only populates after an
 * agent is already open — covered here by the projection-isolation
 * smoke below.
 *
 * This spec covers:
 *
 *   1. Each workspace's projection is independent — a layout edit in
 *      W1 does not reach a client viewing W2.
 *   2. The lifecycle event for a freshly-created workspace propagates
 *      to all userevents subscribers via the `/ws/userevents` stream.
 */

test.describe('Cross-workspace projection isolation', () => {
  test('a layout edit in W1 does not reach a client viewing W2', async ({ browser, leapmuxServer }) => {
    const { hubUrl, adminToken } = leapmuxServer
    // The suite reset deletes these workspaces, and the marker below, before the next test.
    const ws1 = await createWorkspaceViaAPI(hubUrl, adminToken, 'iso-W1')
    const ws2 = await createWorkspaceViaAPI(hubUrl, adminToken, 'iso-W2')
    await withExtraClients(browser, leapmuxServer, 2, async ([pageA, pageB]) => {
      await Promise.all([
        gotoWorkspace(pageA, adminToken, ws1),
        gotoWorkspace(pageB, adminToken, ws2),
      ])

      // Both workspaces start with one tile.
      await expect(tiles(pageA)).toHaveCount(1)
      await expect(tiles(pageB)).toHaveCount(1)

      // Split in W1 — W2's view must remain a single tile. The hub commits
      // the split before the layout-saved event of the client that sent it.
      const saved = waitForLayoutSave(pageA)
      await pageA.locator('[data-testid="split-horizontal"]').first().click()
      await expect(tiles(pageA)).toHaveCount(2)
      await saved

      // Then a marker event that reaches every subscriber: a new workspace.
      // The hub sends each subscriber its events in commit order, so B
      // receives any cross-talk from the split before the marker, and the
      // marker row in B's sidebar ends the window.
      const marker = await createWorkspaceViaAPI(hubUrl, adminToken, 'iso-marker')
      await expect(workspaceRow(pageB, marker), 'the marker event reached B after the split').toBeVisible()
      await expect(tiles(pageB)).toHaveCount(1)
    })
  })

  test('a workspace created in one client appears in another client subscribed to userevents', async ({ page, emptyWorkspace, leapmuxServer }) => {
    const { hubUrl, adminToken } = leapmuxServer
    await gotoWorkspace(page, adminToken, emptyWorkspace.workspaceId)

    // Create a sibling workspace via the hub API; the userevents WS
    // stream should deliver `WorkspaceCreated` and the sidebar
    // should pick it up. The sidebar's row is keyed off the
    // workspace list, which the UserCRDT lifecycle events feed.
    const newWsTitle = 'sibling-via-userevents-stream'
    const newWsId = await createWorkspaceViaAPI(hubUrl, adminToken, newWsTitle)
    await expect(workspaceRow(page, newWsId)).toContainText(newWsTitle)
  })
})
