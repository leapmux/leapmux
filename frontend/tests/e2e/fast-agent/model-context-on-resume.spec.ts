import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { fastAgentTest } from '../fastagent-fixtures'
import { resumePickerScenario } from '../helpers/nativeResumePicker'

fastAgentTest.describe('Fast Agent session resume', () => {
  const provider: AgentProvider = AgentProvider.FAST_AGENT
  const label = 'Fast Agent'
  fastAgentTest('continues a closed session chosen from the native picker', async ({ page, leapmuxServer, modelScript }) => {
    await resumePickerScenario({ page, leapmuxServer, modelScript }, { provider, label })
  })
})
