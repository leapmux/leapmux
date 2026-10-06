import { resumePickerScenario } from '../helpers/nativeResumePicker'
import { kiroTest } from '../kiro-fixtures'
import { nativeContext } from './scenarios'

const label = 'Kiro'

kiroTest.describe(`${label} session resume`, () => {
  kiroTest('continues a closed session chosen from the native picker', async ({ page, leapmuxServer, modelScript }) => {
    await resumePickerScenario({ page, leapmuxServer, modelScript }, nativeContext, { label, sessionList: 'sole-session' })
  })
})
