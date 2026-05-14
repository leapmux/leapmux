import { expect, test } from './fixtures'
import { createWorkspaceViaAPI, deleteWorkspaceViaAPI } from './helpers/api'
import { gotoWorkspace } from './helpers/ui'

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
 * level in `tests/unit/stores/tab.store.crdt.test.ts`
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
 *      to all org-wide subscribers via the `/ws/orgevents` stream.
 */

test.describe('Cross-workspace projection isolation', () => {
  test('a layout edit in W1 does not reach a client viewing W2', async ({ browser, leapmuxServer }) => {
    const { hubUrl, adminToken, adminOrgId } = leapmuxServer
    const ws1 = await createWorkspaceViaAPI(hubUrl, adminToken, 'iso-W1', adminOrgId)
    const ws2 = await createWorkspaceViaAPI(hubUrl, adminToken, 'iso-W2', adminOrgId)
    const ctxA = await browser.newContext({ baseURL: hubUrl })
    const ctxB = await browser.newContext({ baseURL: hubUrl })
    const pageA = await ctxA.newPage()
    const pageB = await ctxB.newPage()
    try {
      await Promise.all([
        gotoWorkspace(pageA, adminToken, `/o/admin/workspace/${ws1}`),
        gotoWorkspace(pageB, adminToken, `/o/admin/workspace/${ws2}`),
      ])

      // Both workspaces start with one tile.
      await expect(pageA.locator('[data-testid="tile"]')).toHaveCount(1)
      await expect(pageB.locator('[data-testid="tile"]')).toHaveCount(1)

      // Split in W1 — W2's view must remain a single tile.
      await pageA.locator('[data-testid="split-horizontal"]').first().click()
      await expect(pageA.locator('[data-testid="tile"]')).toHaveCount(2)

      // Wait long enough for any cross-talk to land if the projection
      // were broken — 750ms is well past the in-process WS round-trip
      // budget (the plan's 500ms window).
      await pageB.waitForTimeout(750)
      await expect(pageB.locator('[data-testid="tile"]')).toHaveCount(1)
    }
    finally {
      await ctxA.close()
      await ctxB.close()
      await deleteWorkspaceViaAPI(hubUrl, adminToken, ws1).catch(() => {})
      await deleteWorkspaceViaAPI(hubUrl, adminToken, ws2).catch(() => {})
    }
  })

  test('a workspace created in one client appears in another client subscribed to the org', async ({ browser, leapmuxServer }) => {
    const { hubUrl, adminToken, adminOrgId } = leapmuxServer
    const seedWs = await createWorkspaceViaAPI(hubUrl, adminToken, 'seed', adminOrgId)
    const ctx = await browser.newContext({ baseURL: hubUrl })
    const page = await ctx.newPage()
    try {
      await gotoWorkspace(page, adminToken, `/o/admin/workspace/${seedWs}`)

      // Create a sibling workspace via the hub API; the org-wide WS
      // stream should deliver `WorkspaceCreated` and the sidebar
      // should pick it up. The sidebar's row is keyed off the
      // workspace registry, which the OrgCRDT lifecycle events feed.
      const newWsTitle = 'sibling-via-org-stream'
      const newWsId = await createWorkspaceViaAPI(hubUrl, adminToken, newWsTitle, adminOrgId)
      try {
        // The workspace switcher / sidebar surfaces titles in both
        // the left and right sidebars; match either. Strict-mode
        // violations on a non-`.first()` locator turn fast lifecycle
        // delivery (both sidebars repopulate before the assertion
        // runs) into a false negative.
        await expect(page.getByText(newWsTitle, { exact: false }).first()).toBeVisible()
      }
      finally {
        await deleteWorkspaceViaAPI(hubUrl, adminToken, newWsId).catch(() => {})
      }
    }
    finally {
      await ctx.close()
      await deleteWorkspaceViaAPI(hubUrl, adminToken, seedWs).catch(() => {})
    }
  })
})
