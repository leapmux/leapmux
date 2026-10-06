import { droidTest } from '../droid-fixtures'
import { resumePickerScenario } from '../helpers/nativeResumePicker'
import { nativeContext } from './scenarios'

droidTest.describe('Factory Droid session resume', () => {
  const label = 'Factory Droid'
  droidTest('continues a closed session chosen from the native picker', async ({ page, leapmuxServer, modelScript }) => {
    await resumePickerScenario({ page, leapmuxServer, modelScript }, nativeContext, { label })
  })
})
