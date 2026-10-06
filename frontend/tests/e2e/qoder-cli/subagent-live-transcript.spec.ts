import { exerciseLiveChildTranscript } from '../helpers/liveChildTranscript'
import { expectRowBecomesFinal } from '../helpers/subagentRegistry'
import { qoderTest } from '../qoder-fixtures'

qoderTest.describe('Qoder CLI live child transcript', () => {
  /** The system prompt of a Qoder child states these words, and the child rule requires them. */
  const CHILD_SYSTEM = 'You are an agent for Qoder'

  qoderTest('shows the child tool result before its final answer', async ({ native, authenticatedQoderWorkspace }) => {
    const child = await exerciseLiveChildTranscript(native, {
      childWhen: { system: CHILD_SYSTEM, user: 'Read the assigned file for the live Qoder child' },
      childTask: 'Read the assigned file for the live Qoder child, then report its marker.',
      parentTask: 'Spawn one subagent to read the child note.',
      toolProof: { read: { workingDir: authenticatedQoderWorkspace.workingDir } },
    })
    await expectRowBecomesFinal(native.page, child.row)
  })
})
