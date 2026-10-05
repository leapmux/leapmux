import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { test } from '../fixtures'
import { resumePickerScenario } from '../helpers/nativeResumePicker'
import { KIRO_E2E_SKIP_REASON } from '../kiro-fixtures'
import { kiroModelTurns } from './modelTurns'

const provider = AgentProvider.KIRO

const label = 'Kiro'

const skip = KIRO_E2E_SKIP_REASON

test.describe(`${label} session resume`, () => {
  test.skip(!!skip, skip ?? '')

  test('continues a closed session chosen from the native picker', async ({ page, leapmuxServer, modelScript }) => {
    await resumePickerScenario({ page, leapmuxServer, modelScript }, {
      provider,
      label,
      sessionList: 'newest-of-three',
      conversationTurns: kiroModelTurns,
    })
  })
})
