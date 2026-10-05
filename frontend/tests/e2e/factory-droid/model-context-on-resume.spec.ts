import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { DROID_E2E_SKIP_REASON, DROID_TITLE_RULE } from '../droid-fixtures'
import { test } from '../fixtures'
import { resumePickerScenario } from '../helpers/nativeResumePicker'

test.describe('Factory Droid session resume', () => {
  const provider: AgentProvider = AgentProvider.DROID
  const label = 'Factory Droid'
  const skip = DROID_E2E_SKIP_REASON
  test.skip(!!skip, skip ?? '')
  test('continues a closed session chosen from the native picker', async ({ page, leapmuxServer, modelScript }) => {
    await resumePickerScenario({ page, leapmuxServer, modelScript }, { provider, label, rules: [DROID_TITLE_RULE], idleTimeoutMs: 180000 })
  })
})
