import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { resumePickerScenario } from '../helpers/nativeResumePicker'
import { kiroTest } from '../kiro-fixtures'
import { kiroModelTurns } from './modelTurns'

const provider = AgentProvider.KIRO

const label = 'Kiro'

kiroTest.describe(`${label} session resume`, () => {
  kiroTest('continues a closed session chosen from the native picker', async ({ page, leapmuxServer, modelScript }) => {
    await resumePickerScenario({ page, leapmuxServer, modelScript }, {
      provider,
      label,
      sessionList: 'newest-of-three',
      conversationTurns: kiroModelTurns,
    })
  })
})
