import { grokTest } from '../grok-fixtures'
import { resumePickerScenario } from '../helpers/nativeResumePicker'
import { nativeContext } from './scenarios'

const label = 'Grok Build'

grokTest.describe(`${label} session resume`, () => {
  grokTest('continues a closed session chosen from the native picker', async ({ page, leapmuxServer, modelScript }) => {
    await resumePickerScenario({ page, leapmuxServer, modelScript }, nativeContext, { label, sessionList: 'sole-session' })
  })
})
