import { expect, test } from './fixtures'
import { createWorkspaceViaAPI, deleteWorkspaceViaAPI, getTestChannel } from './helpers/api'

/**
 * File-tab cross-workspace move with E2EE worker bookkeeping.
 *
 * File tabs are unique because the path lives only on the worker
 * behind the E2EE channel; the hub never sees it. A cross-workspace
 * move is a two-step orchestration:
 *
 *   1. `RelocateFileTabPath(org_id, tab_id, new_workspace_id)` over
 *      the worker's E2EE channel — updates `worker_file_tabs.workspace_id`
 *      and emits `FileTabPathRevoked` on the source workspace's
 *      private-event stream + `FileTabPathRegistered` on the
 *      destination's. There is no `FileTabPathRelocated` event
 *      (would leak destination info to source-only subscribers).
 *   2. CRDT op batch: `SetTabRegister(tab, tile_id=newTileInW2)`.
 *      The hub re-resolves the tab's owning workspace via the new
 *      tile's ancestor chain.
 *
 * The full UI gesture for cross-workspace file-tab move requires a
 * worker-with-worktree fixture that the test runner does not yet
 * provide. This spec covers the *worker side* of the orchestration
 * — the part the plan calls out as needing E2EE round-trip plumbing.
 * The CRDT-side move-as-a-tile_id-write is exercised in the backend
 * cross_workspace_move_test.go integration test.
 */

test.describe('File-tab E2EE worker round-trip', () => {
  test('Register / Get / Relocate / Revoke round-trip honors workspace bookkeeping', async ({ leapmuxServer }) => {
    const { hubUrl, adminToken, adminOrgId, workerId } = leapmuxServer
    const ws1 = await createWorkspaceViaAPI(hubUrl, adminToken, 'file-W1', adminOrgId)
    const ws2 = await createWorkspaceViaAPI(hubUrl, adminToken, 'file-W2', adminOrgId)

    const channel = await getTestChannel(hubUrl, adminToken)

    const {
      RegisterFileTabPathRequestSchema,
      RegisterFileTabPathResponseSchema,
      GetFileTabPathRequestSchema,
      GetFileTabPathResponseSchema,
      RelocateFileTabPathRequestSchema,
      RelocateFileTabPathResponseSchema,
      RevokeFileTabPathRequestSchema,
      RevokeFileTabPathResponseSchema,
    } = await import('../../src/generated/leapmux/v1/workspace_private_pb')

    try {
      // Make both workspaces accessible to the test channel BEFORE
      // calling worker RPCs scoped to them.
      for (const wsId of [ws1, ws2]) {
        const resp = await fetch(`${hubUrl}/leapmux.v1.ChannelService/PrepareWorkspaceAccess`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json', 'Cookie': adminToken },
          body: JSON.stringify({ workerId, workspaceId: wsId }),
        })
        expect(resp.ok).toBeTruthy()
      }

      const tabId = `t-${Date.now()}`
      const filePath = '/repo/test-file.go'

      // 1. Register on W1.
      await channel.callWorker(
        workerId,
        'RegisterFileTabPath',
        RegisterFileTabPathRequestSchema,
        RegisterFileTabPathResponseSchema,
        { orgId: adminOrgId, tabId, workspaceId: ws1, filePath },
      )

      // 2. Get returns the path + workspace_id.
      const got = await channel.callWorker(
        workerId,
        'GetFileTabPath',
        GetFileTabPathRequestSchema,
        GetFileTabPathResponseSchema,
        { orgId: adminOrgId, tabId },
      )
      expect(got.workspaceId).toBe(ws1)
      expect(got.filePath).toBe(filePath)

      // 3. Relocate to W2.
      await channel.callWorker(
        workerId,
        'RelocateFileTabPath',
        RelocateFileTabPathRequestSchema,
        RelocateFileTabPathResponseSchema,
        { orgId: adminOrgId, tabId, newWorkspaceId: ws2 },
      )

      // 4. Get reflects the new workspace; path unchanged.
      const got2 = await channel.callWorker(
        workerId,
        'GetFileTabPath',
        GetFileTabPathRequestSchema,
        GetFileTabPathResponseSchema,
        { orgId: adminOrgId, tabId },
      )
      expect(got2.workspaceId).toBe(ws2)
      expect(got2.filePath).toBe(filePath)

      // 5. Revoke removes the row (subsequent Get returns NotFound).
      await channel.callWorker(
        workerId,
        'RevokeFileTabPath',
        RevokeFileTabPathRequestSchema,
        RevokeFileTabPathResponseSchema,
        { orgId: adminOrgId, tabId },
      )
      let revoked = false
      try {
        await channel.callWorker(
          workerId,
          'GetFileTabPath',
          GetFileTabPathRequestSchema,
          GetFileTabPathResponseSchema,
          { orgId: adminOrgId, tabId },
        )
      }
      catch {
        revoked = true
      }
      expect(revoked).toBe(true)
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, ws1).catch(() => {})
      await deleteWorkspaceViaAPI(hubUrl, adminToken, ws2).catch(() => {})
    }
  })
})
