import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { droidTest } from '../droid-fixtures'
import { exerciseProviderSteer } from '../helpers/providerSteer'

droidTest.describe('Factory Droid mid-turn steering', () => {
  droidTest('places queued guidance in the next native model request', async ({ authenticatedDroidWorkspace, page, modelScript }) => {
    void authenticatedDroidWorkspace
    await exerciseProviderSteer(page, modelScript, AgentProvider.DROID)
  })
})
