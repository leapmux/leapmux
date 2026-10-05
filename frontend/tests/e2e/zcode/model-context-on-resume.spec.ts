import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { test } from '../fixtures'
import { resumePickerScenario } from '../helpers/nativeResumePicker'
import { ZCODE_E2E_SKIP_REASON } from '../zcode-fixtures'

const provider = AgentProvider.ZCODE

const label = 'ZCode'

test.skip(!!ZCODE_E2E_SKIP_REASON, ZCODE_E2E_SKIP_REASON || '')

test('restores old Worker rows and native model context after reopening', async ({ page, leapmuxServer, modelScript }) => {
  await resumePickerScenario({ page, leapmuxServer, modelScript }, { provider, label, assertConversationBubbles: true })
})
