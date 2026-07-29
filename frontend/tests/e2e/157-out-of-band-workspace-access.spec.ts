/**
 * A workspace created by the CLI while the page is already open must still work.
 *
 * A worker channel is told which workspaces it may serve exactly once, in
 * `ChannelOpenRequest.accessible_workspace_ids`, and that set grows only when
 * somebody calls `PrepareWorkspaceAccess`. The browser used to call it from one
 * place -- its own new-workspace dialog. `leapmux remote` never calls it at all
 * and does not need to: it opens a fresh channel per invocation, so its own
 * channel is seeded with the workspace it just created.
 *
 * An already-open browser channel gets neither. Its tabs projected into the
 * sidebar from the CRDT but never hydrated -- the worker answered
 * NOT_ACCESSIBLE to every ListAgents and the client could only re-ask and be
 * refused again, until a reload re-opened the channel with a fresh seed. The
 * client now repairs it instead: NOT_ACCESSIBLE (and the matching
 * PermissionDenied on the private-event stream) triggers
 * `PrepareWorkspaceAccess` for the tab's workspace, then one re-fetch.
 *
 * WHY THIS IS AN E2E. Nothing smaller reproduces it. The defect lives in the
 * seam between three processes -- the hub's open-time seed, the worker's live
 * accessible set, and the browser's hydration -- and each one is individually
 * correct. Three details of the setup are load-bearing, and getting any of them
 * wrong makes the spec pass against the bug:
 *
 *   - the workspace must be created by the CLI, not by `openAgentViaAPI`. That
 *     helper calls `PrepareWorkspaceAccess` with the admin session cookie, and
 *     an unscoped caller's fan-out reaches EVERY channel that user holds on the
 *     worker (`channelWorkspaceUpdateAuthorized` admits `channelScope == ""`) --
 *     including the browser's, which repairs the defect for free;
 *   - the page must not reload afterwards, because `page.goto` re-opens the
 *     channel with a fresh DB seed that already contains the workspace; and
 *   - the assertion must be the TAB LABEL. The pane renders its chat editor
 *     either way and never shows the "Agent not found." placeholder here, so
 *     both of those pass against an unhydrated tab. `tabDisplayLabel` falls back
 *     to the bare string "Agent" with no record; the worker assigns
 *     "Agent <Name>", and only hydration can put that in the DOM.
 */

import type { ServerInfo } from './fixtures'
import { join } from 'node:path'
import { expect, test } from './fixtures'
import {
  cleanupWorkspaceViaAPI,
  createWorkspaceViaAPI,
  deleteWorkspaceViaAPI,
  openAgentViaAPI,
} from './helpers/api'
import { cliAgentOpen, mintCLITokenForAdmin, runCLI } from './helpers/cli'
import { loginViaToken, openWorkspace, tabById, waitForWorkspaceReady } from './helpers/ui'

/** The label a tab carries only once its agent record has arrived. */
const HYDRATED_AGENT_LABEL = /^Agent .+/

/** Dev mode splits the data dir; the admin token command opens the hub side. */
function devModeTokenSource(server: ServerInfo): { hubUrl: string, adminToken: string, dataDir: string } {
  return { hubUrl: server.hubUrl, adminToken: server.adminToken, dataDir: join(server.dataDir, 'hub') }
}

test.describe('out-of-band workspace access', () => {
  test('an agent the CLI opens in a workspace created after page load still hydrates', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId } = leapmuxServer
    const cli = await mintCLITokenForAdmin(devModeTokenSource(leapmuxServer))

    // Workspace ONE exists before the browser starts, so the page has somewhere
    // to land and its channel opens seeded with this workspace only.
    const first = await createWorkspaceViaAPI(hubUrl, adminToken, `access-first-${Date.now()}`)
    let second: string | undefined
    try {
      const firstAgentId = await openAgentViaAPI(hubUrl, adminToken, workerId, first)
      await loginViaToken(page, adminToken)
      await openWorkspace(page, first)
      // This agent hydrating is what proves the browser holds an OPEN channel to
      // the worker -- the precondition the whole spec rests on.
      await expect(tabById(page, firstAgentId)).toHaveText(HYDRATED_AGENT_LABEL)

      // Workspace TWO comes from the CLI, with the page already up. Nothing in
      // this path announces it on the browser's channel.
      const created = await runCLI(cli, [
        'workspace',
        'create',
        '--title',
        `access-cli-${Date.now()}`,
      ]) as { workspace_id?: string } | null
      second = created?.workspace_id
      expect(second, 'the CLI reported the new workspace id').toBeTruthy()
      const agentId = await cliAgentOpen(cli, { workspaceId: second!, workerId })

      // Switch workspaces by clicking the sidebar -- a client-side transition.
      const row = page.locator(`[data-testid="workspace-item-${second}"]`)
      await expect(row, 'the new workspace reaches the sidebar over /ws/userevents').toBeVisible()
      await row.click()
      await expect(row).toHaveAttribute('data-active', 'true')
      await waitForWorkspaceReady(page)

      // The tab projects from the CRDT whether or not it can be hydrated, so its
      // presence proves nothing on its own; its LABEL is the hydration signal.
      const tab = tabById(page, agentId)
      await expect(tab).toBeVisible()
      await expect(
        tab,
        'a bare "Agent" here means the channel was never granted access to the CLI-made workspace',
      ).toHaveText(HYDRATED_AGENT_LABEL)
    }
    finally {
      for (const id of [first, second]) {
        if (!id)
          continue
        await cleanupWorkspaceViaAPI(hubUrl, adminToken, workerId, id).catch(() => {})
        await deleteWorkspaceViaAPI(hubUrl, adminToken, id).catch(() => {})
      }
    }
  })
})
