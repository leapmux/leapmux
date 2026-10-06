import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { DROID_TITLE_RULE, droidTest } from '../droid-fixtures'
import { resumePickerScenario } from '../helpers/nativeResumePicker'

droidTest.describe('Factory Droid session resume', () => {
  const provider: AgentProvider = AgentProvider.DROID
  const label = 'Factory Droid'
  droidTest('continues a closed session chosen from the native picker', async ({ page, leapmuxServer, modelScript }) => {
    await resumePickerScenario({ page, leapmuxServer, modelScript }, { provider, label, rules: [DROID_TITLE_RULE] })
  })
})
