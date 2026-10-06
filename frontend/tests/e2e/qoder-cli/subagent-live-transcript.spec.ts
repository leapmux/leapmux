import { exerciseLiveChildTranscript } from '../helpers/liveChildTranscript'
import { expectRowBecomesFinal } from '../helpers/subagentRegistry'
import { qoderTest } from '../qoder-fixtures'
import { qoderChildTurn } from './childScenario'

qoderTest.describe('Qoder CLI live child transcript', () => {
  qoderTest('shows the child tool result before its final answer', async ({ native, authenticatedQoderWorkspace }) => {
    const child = await exerciseLiveChildTranscript(native, {
      childWhen: qoderChildTurn('Read the assigned file for the live Qoder child'),
      childTask: 'Read the assigned file for the live Qoder child, then report its marker.',
      parentTask: 'Spawn one subagent to read the child note.',
      toolProof: { read: { workingDir: authenticatedQoderWorkspace.workingDir } },
    })
    await expectRowBecomesFinal(native.page, child.row)
  })
})
