import { fastAgentTest } from '../fastagent-fixtures'
import { resumePickerScenario } from '../helpers/nativeResumePicker'
import { nativeContext } from './scenarios'

fastAgentTest.describe('Fast Agent session resume', () => {
  const label = 'Fast Agent'
  fastAgentTest('continues a closed session chosen from the native picker', async ({ page, leapmuxServer, modelScript }) => {
    await resumePickerScenario({ page, leapmuxServer, modelScript }, nativeContext, { label })
  })
})
