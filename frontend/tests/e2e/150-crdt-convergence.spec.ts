import { expect, test } from './fixtures'
import { createWorkspaceViaAPI, deleteWorkspaceViaAPI } from './helpers/api'
import { gotoWorkspace } from './helpers/ui'

/**
 * Multi-client CRDT convergence end-to-end test.
 *
 * Two browser contexts authenticate as the same admin user, navigate
 * to the same workspace, and exercise interleaved layout mutations.
 * Both contexts must converge to the same projected tree because
 * every mutation flows through `/ws/orgevents` as a CRDT op stream.
 *
 * Spec: split-tile in client A → client B sees the new tile shape.
 * Then split-tile in client B → client A sees that, too. Both clients
 * end up with the same set of tile ids (modulo nanoid generation in
 * each client). Convergence guarantee: the projection in both
 * speculative states is byte-equal once both echoes have landed.
 */

test.describe('CRDT convergence', () => {
  test('two contexts split-tile interleaved → both converge', async ({ browser, leapmuxServer }) => {
    const { hubUrl, adminToken, adminOrgId } = leapmuxServer
    const wsId = await createWorkspaceViaAPI(hubUrl, adminToken, 'CRDT Convergence', adminOrgId)
    const workspaceUrl = `/o/admin/workspace/${wsId}`

    const ctxA = await browser.newContext({ baseURL: hubUrl })
    const ctxB = await browser.newContext({ baseURL: hubUrl })
    const pageA = await ctxA.newPage()
    const pageB = await ctxB.newPage()

    try {
      await Promise.all([
        gotoWorkspace(pageA, adminToken, workspaceUrl),
        gotoWorkspace(pageB, adminToken, workspaceUrl),
      ])

      // Initial: each client sees one root leaf. Wait for both.
      await expect(pageA.locator('[data-testid="tile"]')).toHaveCount(1)
      await expect(pageB.locator('[data-testid="tile"]')).toHaveCount(1)

      // Client A: split horizontally. The tile's split-horizontal
      // button is mounted by `Tile.tsx` when `canSplit` is true.
      await pageA.locator('[data-testid="split-horizontal"]').first().click()
      await expect(pageA.locator('[data-testid="tile"]')).toHaveCount(2)
      // Client B should pick up the same tree via /ws/orgevents.
      await expect(pageB.locator('[data-testid="tile"]')).toHaveCount(2)

      // Client B: split the right tile vertically. With two tiles
      // visible after the first split, the second `[data-testid=
      // "split-vertical"]` belongs to the right (newer) sibling.
      await pageB.locator('[data-testid="split-vertical"]').nth(1).click()
      await expect(pageB.locator('[data-testid="tile"]')).toHaveCount(3)
      await expect(pageA.locator('[data-testid="tile"]')).toHaveCount(3)

      // Reload checkpoint: the projection lives in the CRDT, not in
      // sessionStorage, so a refresh must replay it from `OrgMaterialized`
      // and re-derive the same three-tile tree. A future regression that
      // keeps the layout alive only in the local layoutStore (without
      // round-tripping through the hub) would survive in-session
      // assertions but fail here.
      await pageA.reload()
      await pageA.locator('[data-testid="tile"]').first().waitFor()
      await expect(pageA.locator('[data-testid="tile"]')).toHaveCount(3)
      await pageB.reload()
      await pageB.locator('[data-testid="tile"]').first().waitFor()
      await expect(pageB.locator('[data-testid="tile"]')).toHaveCount(3)
    }
    finally {
      await ctxA.close()
      await ctxB.close()
      await deleteWorkspaceViaAPI(hubUrl, adminToken, wsId).catch(() => {})
    }
  })

  test('close-tile from one client tombstones in the other', async ({ browser, leapmuxServer }) => {
    const { hubUrl, adminToken, adminOrgId } = leapmuxServer
    const wsId = await createWorkspaceViaAPI(hubUrl, adminToken, 'CRDT Close', adminOrgId)
    const workspaceUrl = `/o/admin/workspace/${wsId}`

    const ctxA = await browser.newContext({ baseURL: hubUrl })
    const ctxB = await browser.newContext({ baseURL: hubUrl })
    const pageA = await ctxA.newPage()
    const pageB = await ctxB.newPage()

    try {
      await Promise.all([
        gotoWorkspace(pageA, adminToken, workspaceUrl),
        gotoWorkspace(pageB, adminToken, workspaceUrl),
      ])

      // Set up: split into two tiles in A.
      await pageA.locator('[data-testid="split-horizontal"]').first().click()
      await expect(pageA.locator('[data-testid="tile"]')).toHaveCount(2)
      await expect(pageB.locator('[data-testid="tile"]')).toHaveCount(2)

      // Close-tile in B; the right-side tile carries `close-tile`
      // since its sibling is the close-anchor's sibling.
      await pageB.locator('[data-testid="close-tile"]').first().click()
      // After close, the projection's single-child SPLIT collapse
      // rule renders a single tile in both contexts.
      await expect(pageB.locator('[data-testid="tile"]')).toHaveCount(1)
      await expect(pageA.locator('[data-testid="tile"]')).toHaveCount(1)

      // Reload checkpoint: the close-tile undo-split path emits a
      // batch that (a) tombstones the closing tile + sibling and
      // (b) flips the parent's NodeKind back to LEAF. Pre-fix this
      // collapsed locally via the single-child SPLIT projection rule
      // but the projection's "rendered leaf id" was the parent SPLIT
      // node id; if the kind-flip op was dropped on the wire the
      // collapse would un-stick on refresh. Reload re-derives the
      // tree from the hub's confirmed state and proves the LEAF flip
      // committed.
      await pageA.reload()
      await pageA.locator('[data-testid="tile"]').first().waitFor()
      await expect(pageA.locator('[data-testid="tile"]')).toHaveCount(1)
      await pageB.reload()
      await pageB.locator('[data-testid="tile"]').first().waitFor()
      await expect(pageB.locator('[data-testid="tile"]')).toHaveCount(1)
    }
    finally {
      await ctxA.close()
      await ctxB.close()
      await deleteWorkspaceViaAPI(hubUrl, adminToken, wsId).catch(() => {})
    }
  })
})
