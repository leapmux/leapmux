import { expect } from '@playwright/test'
import { droidTest } from '../droid-fixtures'
import { tabById } from '../helpers/ui'
import { exerciseNativeChildTranscript } from './childScenarios'
import { nativeContext } from './scenarios'

droidTest('opens a separate native child tab with the prompt and final archive', async ({ authenticatedDroidWorkspace, page, modelScript, leapmuxServer }, testInfo) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDroidWorkspace.workspaceId })
  const childId = await exerciseNativeChildTranscript(context, testInfo, { followUp: false })
  await expect(tabById(page, childId)).toHaveAttribute('aria-selected', 'true')
})
