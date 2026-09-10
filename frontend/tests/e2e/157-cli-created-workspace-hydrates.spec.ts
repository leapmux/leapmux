/**
 * A CLI-created workspace must hydrate through a channel that already exists.
 * The former announcement protocol fixed a channel workspace set at open time. New workspaces then received NOT_ACCESSIBLE until reload repaired that set.
 * Workers now serve one user and hold no workspace IDs, so channels need no workspace announcement.
 * This test checks the resulting behavior across the hub, worker, and browser.
 *
 * Do not reload the page after creating the second workspace. A reload would replace the channel and hide the original failure.
 * Check the tab label that worker metadata supplies. An unhydrated tab can still render an editor.
 * Only hydration replaces its generic Agent label with the assigned Agent <Name> label.
 */

import type { ServerInfo } from './fixtures'
import { join } from 'node:path'
import { expect, test } from './fixtures'
import {
  createWorkspaceViaAPI,
  deleteWorkspaceViaAPI,
  openAgentViaAPI,
} from './helpers/api'
import { cliAgentOpen, mintCLITokenForAdmin, runCLI } from './helpers/cli'
import { loginViaToken, openWorkspace, tabById, waitForWorkspaceReady, workspaceRow } from './helpers/ui'

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
      const row = workspaceRow(page, second!)
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
        await deleteWorkspaceViaAPI(hubUrl, adminToken, id).catch(() => {})
      }
    }
  })
})
