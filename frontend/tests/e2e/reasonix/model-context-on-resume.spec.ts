import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { test } from '../fixtures'
import { resumePickerScenario } from '../helpers/nativeResumePicker'
import { REASONIX_E2E_SKIP_REASON } from '../reasonix-fixtures'

const provider = AgentProvider.REASONIX

const label = 'Reasonix'

test.skip(!!REASONIX_E2E_SKIP_REASON, REASONIX_E2E_SKIP_REASON || '')

test('restores old Worker rows and native model context after reopening', async ({ page, leapmuxServer, modelScript }) => {
  await resumePickerScenario({ page, leapmuxServer, modelScript }, { provider, label, assertConversationBubbles: true })
})
