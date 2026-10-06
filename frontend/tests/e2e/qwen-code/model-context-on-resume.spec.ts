import { resumePickerScenario } from '../helpers/nativeResumePicker'
import { qwenTest } from '../qwen-fixtures'
import { nativeContext } from './scenarios'

const label = 'Qwen Code'

qwenTest.describe(`${label} session resume`, () => {
  qwenTest('continues a closed session chosen from the native picker', async ({ page, leapmuxServer, modelScript }) => {
    await resumePickerScenario({ page, leapmuxServer, modelScript }, nativeContext, { label, sessionList: 'sole-session' })
  })
})
