import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { test } from '../fixtures'
import { resumePickerScenario } from '../helpers/nativeResumePicker'
import { QODER_E2E_SKIP_REASON } from '../qoder-fixtures'

test.describe('Qoder CLI session resume', () => {
  const provider: AgentProvider = AgentProvider.QODER
  const label = 'Qoder CLI'
  const skip = QODER_E2E_SKIP_REASON
  test.skip(!!skip, skip ?? '')
  test('continues a closed session chosen from the native picker', async ({ page, leapmuxServer, modelScript }) => {
    await resumePickerScenario({ page, leapmuxServer, modelScript }, { provider, label, idleTimeoutMs: 180000 })
  })
})
