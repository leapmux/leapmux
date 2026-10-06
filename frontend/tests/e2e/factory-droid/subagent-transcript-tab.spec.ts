import { expect } from '@playwright/test'
import { droidTest } from '../droid-fixtures'
import { tabById } from '../helpers/ui'
import { exerciseNativeChildTranscript } from './childScenarios'

droidTest('opens a separate native child tab with the prompt and final archive', async ({ native }, testInfo) => {
  const childId = await exerciseNativeChildTranscript(native, testInfo, { followUp: false })
  await expect(tabById(native.page, childId)).toHaveAttribute('aria-selected', 'true')
})
