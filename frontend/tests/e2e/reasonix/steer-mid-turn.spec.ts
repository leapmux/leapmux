import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseProviderSteer } from '../helpers/providerSteer'
import { reasonixTest } from '../reasonix-fixtures'

reasonixTest('places queued guidance in the next native model request', async ({ authenticatedReasonixWorkspace, page, modelScript }) => {
  void authenticatedReasonixWorkspace
  await exerciseProviderSteer(page, modelScript, AgentProvider.REASONIX)
})
