import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { resumePickerScenario } from '../helpers/nativeResumePicker'
import { qwenTest } from '../qwen-fixtures'

const provider = AgentProvider.QWEN_CODE

const label = 'Qwen Code'

qwenTest.describe(`${label} session resume`, () => {
  qwenTest('continues a closed session chosen from the native picker', async ({ page, leapmuxServer, modelScript }) => {
    await resumePickerScenario({ page, leapmuxServer, modelScript }, { provider, label, sessionList: 'newest-of-three' })
  })
})
