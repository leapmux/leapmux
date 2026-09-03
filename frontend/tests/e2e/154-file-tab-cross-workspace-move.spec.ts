import { fileTabPayload } from '~/lib/tabPayload'
import { expect, test } from './fixtures'
import { createWorkspaceViaAPI, deleteWorkspaceViaAPI, getTestChannel } from './helpers/api'

/**
 * File-tab path bookkeeping over the E2EE channel.
 *
 * File tabs are unique because the path lives only on the worker behind the
 * E2EE channel; the hub never sees it. The worker keys that row by
 * `(user_id, tab_id)` and nothing else -- there is no workspace column and no
 * Relocate RPC, because a cross-workspace move is now a pure CRDT tile write
 * with no worker leg at all. That is what makes the move work with the worker
 * offline; it is also why this spec asserts the row survives such a move
 * untouched rather than following it.
 *
 * The CRDT-side move-as-a-tile_id-write is exercised in the backend
 * cross_workspace_move_test.go integration test and in
 * frontend/src/components/shell/crossWorkspaceMove.test.ts.
 */

test.describe('file-tab E2EE worker round-trip', () => {
  test('register / get / revoke round-trip is keyed by tab id alone', async ({ leapmuxServer }) => {
    const { hubUrl, adminToken, workerId } = leapmuxServer
    const ws1 = await createWorkspaceViaAPI(hubUrl, adminToken, 'file-W1')

    const channel = await getTestChannel(hubUrl, adminToken)

    const {
      RegisterTabPayloadRequestSchema,
      RegisterTabPayloadResponseSchema,
      GetTabPayloadRequestSchema,
      GetTabPayloadResponseSchema,
      RevokeTabPayloadRequestSchema,
      RevokeTabPayloadResponseSchema,
    } = await import('../../src/generated/proto/leapmux/v1/worker_private_pb')

    try {
      const tabId = `t-${Date.now()}`
      const filePath = '/repo/test-file.go'

      // 1. Register. No workspace is named -- the worker has nowhere to put one.
      await channel.callWorker(
        workerId,
        'RegisterTabPayload',
        RegisterTabPayloadRequestSchema,
        RegisterTabPayloadResponseSchema,
        { tabId, payload: fileTabPayload(filePath, '') },
      )

      // 2. Get returns the path.
      const got = await channel.callWorker(
        workerId,
        'GetTabPayload',
        GetTabPayloadRequestSchema,
        GetTabPayloadResponseSchema,
        { tabId },
      )
      expect(got.payload?.kind.case === 'file' && got.payload.kind.value.filePath).toBe(filePath)

      // 3. Revoke removes the row (subsequent Get returns NotFound).
      await channel.callWorker(
        workerId,
        'RevokeTabPayload',
        RevokeTabPayloadRequestSchema,
        RevokeTabPayloadResponseSchema,
        { tabId },
      )
      let revoked = false
      try {
        await channel.callWorker(
          workerId,
          'GetTabPayload',
          GetTabPayloadRequestSchema,
          GetTabPayloadResponseSchema,
          { tabId },
        )
      }
      catch {
        revoked = true
      }
      expect(revoked).toBe(true)
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, ws1).catch(() => {})
    }
  })
})
