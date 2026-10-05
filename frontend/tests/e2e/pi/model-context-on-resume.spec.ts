import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { test } from '../fixtures'
import { resumePickerScenario } from '../helpers/nativeResumePicker'
import { PI_E2E_SKIP_REASON } from '../pi-fixtures'

const provider = AgentProvider.PI

const label = 'Pi'

test.skip(!!PI_E2E_SKIP_REASON, PI_E2E_SKIP_REASON || '')

test('restores old Worker rows and native model context after reopening', async ({ page, leapmuxServer, modelScript }) => {
  await resumePickerScenario({ page, leapmuxServer, modelScript }, { provider, label, assertConversationBubbles: true })
})
