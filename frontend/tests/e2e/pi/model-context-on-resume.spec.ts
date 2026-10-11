import { resumePickerScenario } from '../helpers/nativeResumePicker'
import { piTest } from '../pi-fixtures'
import { nativeContext } from './scenarios'

const label = 'Pi'

piTest('restores old Worker rows and native model context after reopening', async ({ page, leapmuxServer, modelScript }) => {
  await resumePickerScenario({ page, leapmuxServer, modelScript }, nativeContext, { label })
})
