import { resumePickerScenario } from '../helpers/nativeResumePicker'
import { kimiTest } from '../kimi-fixtures'
import { nativeContext } from './scenarios'

const label = 'Kimi Code'

kimiTest.describe(`${label} session resume`, () => {
  kimiTest('continues a closed session chosen from the native picker', async ({ page, leapmuxServer, modelScript }) => {
    await resumePickerScenario({ page, leapmuxServer, modelScript }, nativeContext, { label, sessionList: 'sole-session' })
  })
})
