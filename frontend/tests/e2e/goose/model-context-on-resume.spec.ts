import { gooseTest } from '../goose-fixtures'
import { resumePickerScenario } from '../helpers/nativeResumePicker'
import { nativeContext } from './scenarios'

const label = 'Goose'

gooseTest('restores old Worker rows and native model context after reopening', async ({ page, leapmuxServer, modelScript }) => {
  await resumePickerScenario({ page, leapmuxServer, modelScript }, nativeContext, { label, assertConversationBubbles: true })
})
