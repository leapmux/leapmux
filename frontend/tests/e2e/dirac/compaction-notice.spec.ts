import { expect } from '@playwright/test'
import { diracTest } from '../dirac-fixtures'
import { messageContents } from '../helpers/ui'
import { exerciseNativeCondense } from './compactionScenarios'
import { nativeContext } from './scenarios'

diracTest('keeps the completed native compaction status after reload', async ({ authenticatedDiracWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDiracWorkspace.workspaceId })
  await exerciseNativeCondense(context)
  await expect(messageContents(page).filter({ hasText: 'Conversation Condensed' }).first()).toBeVisible()
  await page.reload()
  await expect(messageContents(page).filter({ hasText: 'Conversation Condensed' }).first()).toBeVisible()
})
