import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { resumePickerScenario } from '../helpers/nativeResumePicker'
import { ohMyPiTest } from '../ohmypi-fixtures'

const provider = AgentProvider.OH_MY_PI

const label = 'Oh My Pi'

ohMyPiTest.describe(`${label} session resume`, () => {
  ohMyPiTest('continues a closed session chosen from the native picker', async ({ page, leapmuxServer, modelScript }) => {
    await resumePickerScenario({ page, leapmuxServer, modelScript }, { provider, label, sessionList: 'newest-of-three' })
  })
})
