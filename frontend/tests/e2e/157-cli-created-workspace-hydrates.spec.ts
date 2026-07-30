/**
 * A workspace created by the CLI while the page is already open must still work.
 *
 * This used to be a bug with a whole protocol behind it. A worker channel was
 * told which workspaces it may serve exactly once, at OpenChannel time, and
 * that set grew only when somebody called `PrepareWorkspaceAccess`. A workspace
 * that came into existence AFTER a channel opened was invisible to it: the tabs
 * projected into the sidebar from the CRDT but never hydrated, because the
 * worker answered NOT_ACCESSIBLE to every ListAgents and the client could only
 * re-ask and be refused again until a reload re-seeded the channel.
 *
 * The announcement protocol is gone. A worker serves exactly one user and
 * stores no workspace id, so a channel carries no workspace set to be stale
 * about -- there is nothing to announce and nothing to repair. This spec stays
 * because the USER-VISIBLE outcome is still worth pinning: an agent the CLI
 * opens in a workspace created after page load hydrates, with no reload.
 *
 * WHY THIS IS AN E2E. Nothing smaller covers the seam between three processes
 * -- the hub's channel open, the worker's authorization, and the browser's
 * hydration. Two details of the setup are load-bearing:
 *
 *   - the page must not reload afterwards, because `page.goto` re-opens the
 *     channel and would hide a regression that only affects an already-open
 *     one; and
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

test.describe('cli-created workspace hydrates', () => {
  test('an agent the CLI opens in a workspace created after page load still hydrates', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId } = leapmuxServer
    const cli = await mintCLITokenForAdmin(devModeTokenSource(leapmuxServer))

    // Workspace ONE exists before the browser starts, so the page has somewhere
    // to land and opens its worker channel while only this workspace exists.
    const first = await createWorkspaceViaAPI(hubUrl, adminToken, `access-first-${Date.now()}`)
    let second: string | undefined
    try {
      const firstAgentId = await openAgentViaAPI(hubUrl, adminToken, workerId, first)
      await loginViaToken(page, adminToken)
      await openWorkspace(page, first)
      // This agent hydrating is what proves the browser holds an OPEN channel to
      // the worker -- the precondition the whole spec rests on.
      await expect(tabById(page, firstAgentId)).toHaveText(HYDRATED_AGENT_LABEL)

      // Workspace TWO comes from the CLI, with the page already up. The
      // browser's channel was opened before it existed.
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
        'a bare "Agent" here means the already-open channel could not serve the CLI-made workspace',
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
