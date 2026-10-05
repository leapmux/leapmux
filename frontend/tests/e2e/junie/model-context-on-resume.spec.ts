import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { test } from '../fixtures'
import { resumePickerScenario } from '../helpers/nativeResumePicker'
import { junieAnswerToolCall } from '../helpers/providerToolCalls'
import { JUNIE_E2E_SKIP_REASON } from '../junie-fixtures'
import { junieModelTurns } from './modelTurns'

test.describe('Junie session resume', () => {
  const provider: AgentProvider = AgentProvider.JUNIE
  const label = 'Junie'
  const skip = JUNIE_E2E_SKIP_REASON
  test.skip(!!skip, skip ?? '')
  test('continues a closed session chosen from the native picker', async ({ page, leapmuxServer, modelScript }) => {
    await resumePickerScenario({ page, leapmuxServer, modelScript }, {
      provider,
      label,
      rules: [
        { name: 'junie-capability-filter', when: { system: 'capability filter agent' }, respond: { text: '' } },
        { name: 'junie-task-name', when: { system: 'task description summarizer' }, respond: { text: 'Resume task' } },
      ],
      answerStep: (texts, turn) => turn === 'original'
        ? { toolCalls: [junieAnswerToolCall('junie-resume-first', texts.originalAnswer)] }
        : { toolCalls: [junieAnswerToolCall('junie-resume-second', texts.resumedAnswer)] },
      // Junie compresses the prior exchange into its previous_issue row; the reader
      // splits that compression back into the ordered turns the proof expects.
      conversationTurns: junieModelTurns,
      idleTimeoutMs: 180000,
    })
  })
})
