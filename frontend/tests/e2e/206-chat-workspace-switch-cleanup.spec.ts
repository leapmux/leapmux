import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'
import { createWorkspaceViaAPI, deleteWorkspaceViaAPI, openAgentViaAPI } from './helpers/api'
import { CHAT_SCROLL_CONTAINER, loginViaToken, openWorkspace, waitForWorkspaceReady, workspaceChevron, workspaceRow } from './helpers/ui'

async function clickAgentLeaf(page: Page, workspaceId: string): Promise<void> {
  await page.evaluate((id) => {
    const workspace = document.querySelector(`[data-testid="workspace-item-${id}"]`)
    const leaf = workspace?.nextElementSibling?.querySelector('[data-testid="tab-tree-leaf"]')
    if (!(leaf instanceof HTMLElement))
      throw new Error('The workspace agent tab is not available')
    leaf.click()
  }, workspaceId)
}

test.describe('chat workspace switch cleanup', () => {
  test('switching from a sidebar agent tab does not throw during chat cleanup', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId } = leapmuxServer
    const firstWorkspace = await createWorkspaceViaAPI(hubUrl, adminToken, 'Cleanup Source')
    const secondWorkspace = await createWorkspaceViaAPI(hubUrl, adminToken, 'Cleanup Target')
    await openAgentViaAPI(hubUrl, adminToken, workerId, firstWorkspace)
    await openAgentViaAPI(hubUrl, adminToken, workerId, secondWorkspace)

    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, firstWorkspace)
      await expect(page.locator(CHAT_SCROLL_CONTAINER).filter({ visible: true })).toBeVisible()

      await workspaceChevron(page, secondWorkspace).click()
      const pageErrors: string[] = []
      page.on('pageerror', error => pageErrors.push(error.stack ?? error.message))

      // The click saves the source viewport and replaces its keyed workspace tree
      // in one Solid update. The old chat cleanup must use its cached inputs.
      await clickAgentLeaf(page, secondWorkspace)
      await waitForWorkspaceReady(page)
      await expect(workspaceRow(page, secondWorkspace)).toHaveAttribute('data-active', 'true')
      await expect(page.locator(CHAT_SCROLL_CONTAINER).filter({ visible: true })).toBeVisible()

      expect(pageErrors).toEqual([])
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, firstWorkspace).catch(() => {})
      await deleteWorkspaceViaAPI(hubUrl, adminToken, secondWorkspace).catch(() => {})
    }
  })
})
