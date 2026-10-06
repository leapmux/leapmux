import { expect } from '@playwright/test'
import { diracTest } from '../dirac-fixtures'
import { messageContents } from '../helpers/ui'
import { exerciseNativeCondense } from './compactionScenarios'

diracTest('keeps the completed native compaction status after reload', async ({ native }) => {
  await exerciseNativeCondense(native)
  await expect(messageContents(native.page).filter({ hasText: 'Conversation Condensed' }).first()).toBeVisible()
  await native.page.reload()
  await expect(messageContents(native.page).filter({ hasText: 'Conversation Condensed' }).first()).toBeVisible()
})
