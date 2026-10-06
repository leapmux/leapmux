import { codexTest } from '../codex-fixtures'
import { resumePickerScenario } from '../helpers/nativeResumePicker'
import { nativeContext } from './scenarios'

const label = 'Codex'

codexTest.describe(`${label} session resume`, () => {
  codexTest('continues a closed session chosen from the native picker', async ({ page, leapmuxServer, modelScript }) => {
    await resumePickerScenario({ page, leapmuxServer, modelScript }, nativeContext, { label, sessionList: 'sole-session' })
  })
})
