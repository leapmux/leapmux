import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { CODEBUDDY_E2E_SKIP_REASON } from '../codebuddy-fixtures'
import { test } from '../fixtures'
import { resumePickerScenario } from '../helpers/nativeResumePicker'

test.describe('CodeBuddy Code session resume', () => {
  const provider: AgentProvider = AgentProvider.CODEBUDDY

  const label = 'CodeBuddy Code'

  const skip = CODEBUDDY_E2E_SKIP_REASON

  test.skip(!!skip, skip ?? '')

  test('continues a closed session chosen from the native picker', async ({ page, leapmuxServer, modelScript }) => {
    await resumePickerScenario({ page, leapmuxServer, modelScript }, { provider, label, sessionList: 'newest-of-three' })
  })
})
