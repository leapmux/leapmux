import { resumePickerScenario } from '../helpers/nativeResumePicker'
import { mimoTest } from '../mimo-fixtures'
import { nativeContext } from './scenarios'

const label = 'MiMo Code'

mimoTest.describe(`${label} session resume`, () => {
  mimoTest('continues a closed session chosen from the native picker', async ({ page, leapmuxServer, modelScript }) => {
    await resumePickerScenario({ page, leapmuxServer, modelScript }, nativeContext, { label, sessionList: 'sole-session' })
  })
})
