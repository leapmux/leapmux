import { copilotTest } from '../copilot-fixtures'
import { exerciseLiveChildTranscript } from '../helpers/liveChildTranscript'
import { expectRowBecomesFinal } from '../helpers/subagentRegistry'
import { waitForAgentIdle, waitForSettingsIdle } from '../helpers/ui'
import { copilotChildTaskMatcher } from './childIdentity'
import { bypassToolRequests } from './scenarios'

copilotTest('shows a child tool before the child finishes', async ({ native }) => {
  const { page } = native
  await bypassToolRequests(native)
  await waitForSettingsIdle(page)
  const child = await exerciseLiveChildTranscript(native, {
    childWhen: copilotChildTaskMatcher('Run printf copilot-child-live'),
    childTask: 'Run printf copilot-child-live, then report the result.',
    parentTask: 'Spawn one child to run the shell probe.',
    toolProof: { shell: { command: 'printf copilot-child-live' } },
    childResponse: { text: 'COPILOT_CHILD_LIVE_DONE' },
  })
  await waitForAgentIdle(page)
  await expectRowBecomesFinal(page, child.row)
})
