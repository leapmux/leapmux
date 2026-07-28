import { expect, test } from './fixtures'
import { createWorkspaceViaAPI, deleteWorkspaceViaAPI } from './helpers/api'
import { expectAnyVisible, loginViaToken, waitForWorkspaceReady } from './helpers/ui'

test.describe('Workspace Lifecycle', () => {
  test('should create multiple workspaces and show all in sidebar', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken } = leapmuxServer
    const workspaceIds: string[] = []
    workspaceIds.push(await createWorkspaceViaAPI(hubUrl, adminToken, 'Lifecycle WS Alpha'))
    workspaceIds.push(await createWorkspaceViaAPI(hubUrl, adminToken, 'Lifecycle WS Beta'))
    workspaceIds.push(await createWorkspaceViaAPI(hubUrl, adminToken, 'Lifecycle WS Gamma'))
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/workspace/${workspaceIds[0]}`)
      await waitForWorkspaceReady(page)

      // All three workspaces should appear in the sidebar
      await expect(page.getByText('Lifecycle WS Alpha')).toBeVisible()
      await expect(page.getByText('Lifecycle WS Beta')).toBeVisible()
      await expect(page.getByText('Lifecycle WS Gamma')).toBeVisible()
    }
    finally {
      for (const id of workspaceIds) {
        await deleteWorkspaceViaAPI(hubUrl, adminToken, id).catch(() => {})
      }
    }
  })

  test('should handle workspace with special characters in title', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Test - My_Workspace 2.0')
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      // The workspace with special characters should appear correctly in the sidebar
      await expect(page.getByText('Test - My_Workspace 2.0')).toBeVisible()
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  test('should show workspace list or empty state on app home', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.adminToken)

    // Navigate to the app home without creating any workspaces in this test.
    // Other tests may have created workspaces, so we check for either state.
    await page.goto('/')

    // Wait for the sidebar to load - it should show either an empty prompt
    // or a section header (In progress / Archived) from the workspace list.
    // Use data-testid selectors to avoid strict mode violations from
    // text matches in context menus or other UI elements.
    await expectAnyVisible(
      page.locator('[data-testid="create-workspace-button"]'),
      page.locator('[data-testid="section-header-workspaces_in_progress"]'),
      page.locator('[data-testid="section-header-workspaces_archived"]'),
    )
  })
})
