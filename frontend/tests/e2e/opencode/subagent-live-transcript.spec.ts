import { exerciseLiveChildTranscript } from '../helpers/liveChildTranscript'
import { expectRowBecomesFinal } from '../helpers/subagentRegistry'
import { opencodeTest } from '../opencode-fixtures'
import { OPENCODE_CHILD } from './childScenario'

// OpenCode streams a child session only to a client that loaded it. The Worker loads it once the parent's task call
// states the session, so the child's own tool calls and text reach the child tab while it runs.
opencodeTest('shows the child tool result before its final answer', async ({ native, authenticatedOpencodeWorkspace }) => {
  const child = await exerciseLiveChildTranscript(native, {
    childWhen: OPENCODE_CHILD.childTask('Read the assigned file for the live OpenCode child'),
    childTask: 'Read the assigned file for the live OpenCode child, then report its marker.',
    parentTask: 'Spawn one subagent to read the child note.',
    toolProof: {
      read: { workingDir: authenticatedOpencodeWorkspace.workingDir },
    },
  })
  await expectRowBecomesFinal(native.page, child.row)
})

// Track the native route and its LeapMux integration: https://github.com/leapmux/leapmux/issues/516.
