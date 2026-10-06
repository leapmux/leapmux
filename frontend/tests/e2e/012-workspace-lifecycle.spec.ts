import { expect, test } from './fixtures'
import { createWorkspaceViaAPI } from './helpers/api'
import { expectAnyVisible, loginViaToken, openAppAs, openWorkspace, sidebarSectionHeader, workspaceRow } from './helpers/ui'

test.describe('Workspace Lifecycle', () => {
  test('should create multiple workspaces and show all in sidebar', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken } = leapmuxServer
    // The titles are the subject, so each workspace states its own. The per-test
    // reset of the fixtures deletes them before the next test.
    const workspaces: Array<{ id: string, title: string }> = []
    for (const title of ['Lifecycle WS Alpha', 'Lifecycle WS Beta', 'Lifecycle WS Gamma'])
      workspaces.push({ id: await createWorkspaceViaAPI(hubUrl, adminToken, title), title })

    await loginViaToken(page, adminToken)
    await openWorkspace(page, workspaces[0]!.id)

    // All three workspaces should appear in the sidebar, each in its own row.
    for (const { id, title } of workspaces)
      await expect(workspaceRow(page, id)).toContainText(title)
  })

  test('should handle workspace with special characters in title', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Test - My_Workspace 2.0')

    await loginViaToken(page, adminToken)
    await openWorkspace(page, workspaceId)

    // The workspace with special characters should appear correctly in the sidebar
    await expect(workspaceRow(page, workspaceId)).toContainText('Test - My_Workspace 2.0')
  })

  test('should show workspace list or empty state on app home', async ({ page, leapmuxServer }) => {
    // Load the app home. The test creates no workspace.
    await openAppAs(page, leapmuxServer.adminToken)

    // The sidebar shows either an empty prompt or a section header (In
    // progress / Archived) of the workspace list. Test IDs, not text, because a
    // text match can also find a context menu item. The sidebar mounts twice
    // (desktop and mobile), so each locator takes the visible copy.
    await expectAnyVisible(
      page.locator('[data-testid="create-workspace-button"]:visible').first(),
      sidebarSectionHeader(page, 'workspaces_in_progress'),
      sidebarSectionHeader(page, 'workspaces_archived'),
    )
  })
})
