import { droidTest } from '../droid-fixtures'
import { exerciseNativeChildTranscript } from './childScenarios'
import { nativeContext } from './scenarios'

droidTest.describe('factory Droid subagents', () => {
  droidTest('opens a live child transcript and sends a follow-up from its tab', async ({ authenticatedDroidWorkspace, leapmuxServer, page, modelScript }, testInfo) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDroidWorkspace.workspaceId })
    await exerciseNativeChildTranscript(context, testInfo, { followUp: true })
  })
})
