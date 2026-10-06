import { OPTION_ID_PERMISSION_MODE } from '../../../src/components/chat/settingsGroups'
import { COPILOT_PERMISSION_MODE } from '../../../src/generated/contracts/copilot-protocol'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { copilotTest } from '../copilot-fixtures'
import { resumePickerScenario } from '../helpers/nativeResumePicker'

const provider = AgentProvider.GITHUB_COPILOT

const label = 'Copilot'

copilotTest('restores old Worker rows and native model context after reopening', async ({ page, leapmuxServer, modelScript }) => {
  await resumePickerScenario({ page, leapmuxServer, modelScript }, {
    provider,
    label,
    assertConversationBubbles: true,
    subjectOptionValues: { [OPTION_ID_PERMISSION_MODE]: COPILOT_PERMISSION_MODE.Manual },
  })
})
