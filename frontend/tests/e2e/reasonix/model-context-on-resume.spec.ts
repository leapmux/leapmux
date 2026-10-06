import { resumePickerScenario } from '../helpers/nativeResumePicker'
import { reasonixTest } from '../reasonix-fixtures'
import { nativeContext } from './scenarios'

const label = 'Reasonix'

reasonixTest('restores old Worker rows and native model context after reopening', async ({ page, leapmuxServer, modelScript }) => {
  await resumePickerScenario({ page, leapmuxServer, modelScript }, nativeContext, { label, assertConversationBubbles: true })
})
