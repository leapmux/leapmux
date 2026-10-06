import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codebuddyTest } from '../codebuddy-fixtures'
import { resumePickerScenario } from '../helpers/nativeResumePicker'

codebuddyTest.describe('CodeBuddy Code session resume', () => {
  const provider: AgentProvider = AgentProvider.CODEBUDDY

  const label = 'CodeBuddy Code'

  codebuddyTest('continues a closed session chosen from the native picker', async ({ page, leapmuxServer, modelScript }) => {
    await resumePickerScenario({ page, leapmuxServer, modelScript }, { provider, label, sessionList: 'newest-of-three' })
  })
})
