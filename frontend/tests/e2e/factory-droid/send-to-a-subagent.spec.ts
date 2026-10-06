import { droidTest } from '../droid-fixtures'
import { exerciseNativeChildTranscript } from './childScenarios'

droidTest.describe('factory Droid subagents', () => {
  droidTest('opens a live child transcript and sends a follow-up from its tab', async ({ native }, testInfo) => {
    await exerciseNativeChildTranscript(native, testInfo, { followUp: true })
  })
})
