import { expect } from '@playwright/test'
import { droidTest } from '../droid-fixtures'
import { tabById } from '../helpers/ui'
import { exerciseNativeChildTranscript } from './childScenarios'
import { nativeContext } from './scenarios'

droidTest('shows the actual child Read before the final native report', async ({ authenticatedDroidWorkspace, page, modelScript, leapmuxServer }, testInfo) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDroidWorkspace.workspaceId })
  const childId = await exerciseNativeChildTranscript(context, testInfo, { followUp: false })
  await expect(tabById(page, childId)).toHaveAttribute('aria-selected', 'true')
})
