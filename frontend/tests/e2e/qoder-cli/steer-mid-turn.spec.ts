import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseProviderSteer } from '../helpers/providerSteer'
import { qoderTest } from '../qoder-fixtures'

qoderTest.describe('Qoder CLI live child transcript', () => {
  const QODER = AgentProvider.QODER

  qoderTest('sends a queued message into the active turn', async ({ qoderWorkspace, page, modelScript }) => {
    void qoderWorkspace
    await exerciseProviderSteer(page, modelScript, QODER)
  })
})
