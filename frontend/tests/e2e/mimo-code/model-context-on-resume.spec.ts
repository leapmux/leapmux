import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { resumePickerScenario } from '../helpers/nativeResumePicker'
import { mimoTest } from '../mimo-fixtures'

const provider = AgentProvider.MIMO_CODE

const label = 'MiMo Code'

mimoTest.describe(`${label} session resume`, () => {
  mimoTest('continues a closed session chosen from the native picker', async ({ page, leapmuxServer, modelScript }) => {
    await resumePickerScenario({ page, leapmuxServer, modelScript }, { provider, label, sessionList: 'newest-of-three' })
  })
})
