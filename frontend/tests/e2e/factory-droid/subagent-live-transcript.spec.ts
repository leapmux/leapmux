import { expect } from '@playwright/test'
import { droidTest } from '../droid-fixtures'
import { tabById } from '../helpers/ui'
import { exerciseNativeChildTranscript } from './childScenarios'

// The scenario proves the Read of the child in its tab before the final report, and the order of the rows.
droidTest('shows the actual child Read before the final native report', async ({ native }, testInfo) => {
  const childId = await exerciseNativeChildTranscript(native, testInfo, { followUp: false })
  await expect(tabById(native.page, childId)).toHaveAttribute('aria-selected', 'true')
})
