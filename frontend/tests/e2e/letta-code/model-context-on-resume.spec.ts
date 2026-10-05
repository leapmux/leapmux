import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { test } from '../fixtures'
import { resumePickerScenario } from '../helpers/nativeResumePicker'
import { LETTA_E2E_SKIP_REASON, LETTA_TITLE_RULE } from '../letta-fixtures'

test.describe('Letta Code session resume', () => {
  const provider: AgentProvider = AgentProvider.LETTA
  const label = 'Letta Code'
  const skip = LETTA_E2E_SKIP_REASON
  test.skip(!!skip, skip ?? '')
  test('continues a closed session chosen from the native picker', async ({ page, leapmuxServer, modelScript }) => {
    await resumePickerScenario({ page, leapmuxServer, modelScript }, { provider, label, rules: [LETTA_TITLE_RULE] })
  })
})
