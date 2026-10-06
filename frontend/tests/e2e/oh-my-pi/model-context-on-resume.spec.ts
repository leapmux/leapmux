import { resumePickerScenario } from '../helpers/nativeResumePicker'
import { ohMyPiTest } from '../ohmypi-fixtures'
import { nativeContext } from './scenarios'

const label = 'Oh My Pi'

ohMyPiTest.describe(`${label} session resume`, () => {
  ohMyPiTest('continues a closed session chosen from the native picker', async ({ page, leapmuxServer, modelScript }) => {
    await resumePickerScenario({ page, leapmuxServer, modelScript }, nativeContext, { label, sessionList: 'sole-session' })
  })
})
