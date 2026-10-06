import { resumePickerScenario } from '../helpers/nativeResumePicker'
import { zcodeTest } from '../zcode-fixtures'
import { nativeContext } from './scenarios'

const label = 'ZCode'

zcodeTest('restores old Worker rows and native model context after reopening', async ({ page, leapmuxServer, modelScript }) => {
  await resumePickerScenario({ page, leapmuxServer, modelScript }, nativeContext, { label })
})
