import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { resumePickerScenario } from '../helpers/nativeResumePicker'
import { kimiTest } from '../kimi-fixtures'

const provider = AgentProvider.KIMI_CODE

const label = 'Kimi Code'

kimiTest.describe(`${label} session resume`, () => {
  kimiTest('continues a closed session chosen from the native picker', async ({ page, leapmuxServer, modelScript }) => {
    await resumePickerScenario({ page, leapmuxServer, modelScript }, { provider, label, sessionList: 'newest-of-three' })
  })
})
