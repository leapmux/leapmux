import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseProviderSteer } from '../helpers/providerSteer'
import { piTest } from '../pi-fixtures'

piTest('places queued guidance in the next native model request', async ({ authenticatedPiWorkspace, page, modelScript }) => {
  void authenticatedPiWorkspace
  await exerciseProviderSteer(page, modelScript, AgentProvider.PI)
})
