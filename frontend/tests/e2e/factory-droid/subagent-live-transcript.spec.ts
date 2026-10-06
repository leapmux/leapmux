import { droidTest } from '../droid-fixtures'
import { exerciseNativeChildTranscript } from './childScenarios'

// The scenario proves the Read of the child in its tab before the final report, the order of the rows, and the
// selected tab of the child at the end.
droidTest('shows the actual child Read before the final native report', async ({ native }, testInfo) => {
  await exerciseNativeChildTranscript(native, testInfo, { followUp: false })
})
