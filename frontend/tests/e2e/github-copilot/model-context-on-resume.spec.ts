import { OPTION_ID_PERMISSION_MODE } from '../../../src/components/chat/settingsGroups'
import { COPILOT_PERMISSION_MODE } from '../../../src/generated/contracts/copilot-protocol'
import { copilotTest } from '../copilot-fixtures'
import { resumePickerScenario } from '../helpers/nativeResumePicker'
import { nativeContext } from './scenarios'

const label = 'Copilot'

copilotTest('restores old Worker rows and native model context after reopening', async ({ page, leapmuxServer, modelScript }) => {
  await resumePickerScenario({ page, leapmuxServer, modelScript }, nativeContext, {
    label,
    subjectOptionValues: { [OPTION_ID_PERMISSION_MODE]: COPILOT_PERMISSION_MODE.Manual },
  })
})
