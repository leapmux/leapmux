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

import { expect } from '@playwright/test'
import { test } from './fixtures'
import { cliAgentOpen, mintCLITokenForAdmin, runCLI } from './helpers/cli'
import { tabById, waitForWorkspaceReady, workspaceRow, workspaceRowTitle } from './helpers/ui'

/** The label a tab carries only once its agent record has arrived. */
const HYDRATED_AGENT_LABEL = /^Agent .+/

test.describe('cli-created workspace hydrates', () => {
  // The test deletes no workspace. The per-test reset of the fixtures deletes every workspace before the next test,
  // the one that the CLI creates included, and reports a failed delete.
  test('an agent the CLI opens in a workspace created after page load still hydrates', async ({ page, leapmuxServer, authenticatedWorkspace }) => {
    // Workspace ONE exists before the browser starts, so the page has somewhere
    // to land and opens its worker channel while only this workspace exists.
    // The fixture creates it with its agent, signs in, and shows it.
    // This agent hydrating is what proves the browser holds an OPEN channel to
    // the worker -- the precondition the whole spec rests on.
    await expect(tabById(page, authenticatedWorkspace.agentId)).toHaveText(HYDRATED_AGENT_LABEL)

    // Workspace TWO comes from the CLI, with the page already up. The
    // browser's channel was opened before it existed.
    const cli = await mintCLITokenForAdmin(leapmuxServer)
    const created = await runCLI(cli, [
      'workspace',
      'create',
      '--title',
      `access-cli-${Date.now()}`,
    ]) as { workspace_id?: string } | null
    const second = created?.workspace_id
    if (!second)
      throw new Error('The CLI reported no ID for the workspace that it created.')
    const agentId = await cliAgentOpen(cli, { workspaceId: second, workerId: leapmuxServer.workerId })

    // Switch workspaces by clicking the sidebar -- a client-side transition.
    const row = workspaceRow(page, second)
    await expect(row, 'the new workspace reaches the sidebar over /ws/userevents').toBeVisible()
    await workspaceRowTitle(page, second).click()
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
  })
})
