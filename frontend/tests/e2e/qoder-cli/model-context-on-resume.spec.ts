import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { resumePickerScenario } from '../helpers/nativeResumePicker'
import { qoderTest } from '../qoder-fixtures'

qoderTest.describe('Qoder CLI session resume', () => {
  const provider: AgentProvider = AgentProvider.QODER
  const label = 'Qoder CLI'
  qoderTest('continues a closed session chosen from the native picker', async ({ page, leapmuxServer, modelScript }) => {
    await resumePickerScenario({ page, leapmuxServer, modelScript }, { provider, label })
  })
})
