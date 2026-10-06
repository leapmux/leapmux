import { resumePickerScenario } from '../helpers/nativeResumePicker'
import { kiloTest } from '../kilo-fixtures'
import { nativeContext } from './scenarios'

const label = 'Kilo'

kiloTest('restores old Worker rows and native model context after reopening', async ({ page, leapmuxServer, modelScript }) => {
  await resumePickerScenario({ page, leapmuxServer, modelScript }, nativeContext, { label, assertConversationBubbles: true })
})
