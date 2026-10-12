import { resumePickerScenario } from '../helpers/nativeResumePicker'
import { qoderTest } from '../qoder-fixtures'
import { nativeContext } from './scenarios'

qoderTest.describe('Qoder CLI session resume', () => {
  const label = 'Qoder CLI'
  qoderTest('continues a closed session chosen from the native picker', async ({ page, leapmuxServer, modelScript }) => {
    await resumePickerScenario({ page, leapmuxServer, modelScript }, nativeContext, { label })
  })
})
