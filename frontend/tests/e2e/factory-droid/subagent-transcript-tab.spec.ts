import { droidTest } from '../droid-fixtures'
import { exerciseNativeChildTranscript } from './childScenarios'

// The scenario opens the tab of the child, proves its prompt and its final answer, and ends with that tab selected.
droidTest('opens a separate native child tab with the prompt and final archive', async ({ native }, testInfo) => {
  await exerciseNativeChildTranscript(native, testInfo, { followUp: false })
})
