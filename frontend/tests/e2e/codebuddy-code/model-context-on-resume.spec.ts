import { codebuddyTest } from '../codebuddy-fixtures'
import { resumePickerScenario } from '../helpers/nativeResumePicker'
import { nativeContext } from './scenarios'

codebuddyTest.describe('CodeBuddy Code session resume', () => {
  const label = 'CodeBuddy Code'

  codebuddyTest('continues a closed session chosen from the native picker', async ({ page, leapmuxServer, modelScript }) => {
    await resumePickerScenario({ page, leapmuxServer, modelScript }, nativeContext, { label, sessionList: 'sole-session' })
  })
})
