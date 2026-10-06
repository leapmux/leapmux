import { resumePickerScenario } from '../helpers/nativeResumePicker'
import { junieTest } from '../junie-fixtures'
import { nativeContext } from './scenarios'

junieTest.describe('Junie session resume', () => {
  const label = 'Junie'
  junieTest('continues a closed session chosen from the native picker', async ({ page, leapmuxServer, modelScript }) => {
    await resumePickerScenario({ page, leapmuxServer, modelScript }, nativeContext, { label })
  })
})
