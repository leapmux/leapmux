import { expect, test } from './fixtures'
import { chatScrollContainer, loginViaToken, openWorkspace, sidebarLeaves, waitForWorkspaceReady, workspaceChevron, workspaceRow } from './helpers/ui'
import { createWorkspaceWithAgentsViaAPI } from './helpers/workspace'

test.describe('chat workspace switch cleanup', () => {
  test('switching from a sidebar agent tab does not throw during chat cleanup', async ({ page, leapmuxServer }) => {
    const { workspaceId: firstWorkspace } = await createWorkspaceWithAgentsViaAPI(leapmuxServer, 'Cleanup Source')
    const { workspaceId: secondWorkspace } = await createWorkspaceWithAgentsViaAPI(leapmuxServer, 'Cleanup Target')

    await loginViaToken(page, leapmuxServer.adminToken)
    await openWorkspace(page, firstWorkspace)
    await expect(chatScrollContainer(page)).toBeVisible()

    await workspaceChevron(page, secondWorkspace).click()
    const pageErrors: string[] = []
    page.on('pageerror', error => pageErrors.push(error.stack ?? error.message))

    // The click saves the source viewport and replaces its keyed workspace tree
    // in one Solid update. The old chat cleanup must use its cached inputs.
    // The click is dispatched on the leaf, because the workspace row above it covers it.
    await sidebarLeaves(page, secondWorkspace).first().dispatchEvent('click')
    await waitForWorkspaceReady(page)
    await expect(workspaceRow(page, secondWorkspace)).toHaveAttribute('data-active', 'true')
    await expect(chatScrollContainer(page)).toBeVisible()

    expect(pageErrors).toEqual([])
  })
})
