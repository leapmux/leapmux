import { exerciseLiveChildTranscript } from '../helpers/liveChildTranscript'
import { kimiTest } from '../kimi-fixtures'
import { kimiChildTurn, prepareKimiChildRun } from './childScenario'

kimiTest.describe('runs Kimi Code subagents and background tasks', () => {
  kimiTest('shows the child prompt while the child still runs', async ({ native }) => {
    await exerciseLiveChildTranscript(native, {
      childWhen: kimiChildTurn('CHILD_LIVE_DONE'),
      childTask: 'Reply with CHILD_LIVE_DONE.',
      parentTask: 'Delegate the live child task.',
    })
  })

  // A reminder follows the task as the last user turn of a child request, so the rule matches the task in the body.
  kimiTest('shows a native child file result only in the running child tab', async ({ native, authenticatedKimiWorkspace }) => {
    await exerciseLiveChildTranscript(native, {
      childWhen: kimiChildTurn('CHILD_LIVE_READ_TASK'),
      childTask: 'Read the assigned file for CHILD_LIVE_READ_TASK.',
      parentTask: 'Delegate the live child file read.',
      toolProof: { read: { workingDir: authenticatedKimiWorkspace.workingDir } },
    })
  })

  kimiTest.beforeEach(async ({ native }) => {
    await prepareKimiChildRun(native.page)
  })
})
