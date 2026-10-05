import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { test } from '../fixtures'
import { resumePickerScenario } from '../helpers/nativeResumePicker'
import { KILO_E2E_SKIP_REASON } from '../kilo-fixtures'

const provider = AgentProvider.KILO

const label = 'Kilo'

test.skip(!!KILO_E2E_SKIP_REASON, KILO_E2E_SKIP_REASON || '')

test('restores old Worker rows and native model context after reopening', async ({ page, leapmuxServer, modelScript }) => {
  await resumePickerScenario({ page, leapmuxServer, modelScript }, { provider, label, assertConversationBubbles: true })
})
