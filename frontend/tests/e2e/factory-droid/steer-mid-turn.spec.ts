import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { DROID_TITLE_RULE, droidTest } from '../droid-fixtures'
import { exerciseProviderSteer } from '../helpers/providerSteer'

droidTest.describe('Factory Droid mid-turn steering', () => {
  droidTest('places queued guidance in the next native model request', async ({ authenticatedDroidWorkspace, page, modelScript }) => {
    void authenticatedDroidWorkspace
    await modelScript.rule(DROID_TITLE_RULE)
    await exerciseProviderSteer(page, modelScript, AgentProvider.DROID)
  })
})
