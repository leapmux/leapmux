import { resumePickerScenario } from '../helpers/nativeResumePicker'
import { opencodeTest } from '../opencode-fixtures'
import { nativeContext } from './scenarios'

const label = 'OpenCode'

opencodeTest('restores old Worker rows and native model context after reopening', async ({ page, leapmuxServer, modelScript }) => {
  await resumePickerScenario({ page, leapmuxServer, modelScript }, nativeContext, { label, assertConversationBubbles: true })
})
