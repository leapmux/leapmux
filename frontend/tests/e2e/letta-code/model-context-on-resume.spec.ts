import { resumePickerScenario } from '../helpers/nativeResumePicker'
import { lettaTest } from '../letta-fixtures'
import { nativeContext } from './scenarios'

lettaTest.describe('Letta Code session resume', () => {
  const label = 'Letta Code'
  lettaTest('continues a closed session chosen from the native picker', async ({ page, leapmuxServer, modelScript }) => {
    await resumePickerScenario({ page, leapmuxServer, modelScript }, nativeContext, { label })
  })
})
