import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { test } from '../fixtures'
import { GOOSE_E2E_SKIP_REASON } from '../goose-fixtures'
import { resumePickerScenario } from '../helpers/nativeResumePicker'

const provider = AgentProvider.GOOSE

const label = 'Goose'

test.skip(!!GOOSE_E2E_SKIP_REASON, GOOSE_E2E_SKIP_REASON || '')

test('restores old Worker rows and native model context after reopening', async ({ page, leapmuxServer, modelScript }) => {
  await resumePickerScenario({ page, leapmuxServer, modelScript }, { provider, label, assertConversationBubbles: true })
})
