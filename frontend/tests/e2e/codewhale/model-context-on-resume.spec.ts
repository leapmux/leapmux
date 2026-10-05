import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { CODEWHALE_E2E_SKIP_REASON } from '../codewhale-fixtures'
import { test } from '../fixtures'
import { resumePickerScenario } from '../helpers/nativeResumePicker'

const provider = AgentProvider.CODEWHALE

const label = 'Codewhale'

const skip = CODEWHALE_E2E_SKIP_REASON

test.describe(`${label} session resume`, () => {
  test.skip(!!skip, skip ?? '')

  test('continues a closed session chosen from the native picker', async ({ page, leapmuxServer, modelScript }) => {
    await resumePickerScenario({ page, leapmuxServer, modelScript }, { provider, label, sessionList: 'newest-of-three' })
  })
})
