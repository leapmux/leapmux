import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { FAST_AGENT_E2E_SKIP_REASON } from '../fastagent-fixtures'
import { test } from '../fixtures'
import { resumePickerScenario } from '../helpers/nativeResumePicker'

test.describe('Fast Agent session resume', () => {
  const provider: AgentProvider = AgentProvider.FAST_AGENT
  const label = 'Fast Agent'
  const skip = FAST_AGENT_E2E_SKIP_REASON
  test.skip(!!skip, skip ?? '')
  test('continues a closed session chosen from the native picker', async ({ page, leapmuxServer, modelScript }) => {
    await resumePickerScenario({ page, leapmuxServer, modelScript }, { provider, label, idleTimeoutMs: 180000 })
  })
})
