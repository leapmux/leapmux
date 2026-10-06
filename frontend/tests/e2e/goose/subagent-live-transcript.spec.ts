import { gooseTest } from '../goose-fixtures'
import { exerciseLiveChildTranscript, LIVE_CHILD_SHELL_CALL_ID, LIVE_CHILD_SPAWN_CALL_ID } from '../helpers/liveChildTranscript'
import { goosePermissionJudgmentToolCall } from '../helpers/providerToolCalls'
import { expectRowBecomesFinal } from '../helpers/subagentRegistry'
import { gooseChildTaskMatcher } from './childIdentity'

gooseTest('shows a child tool request before the child finishes', async ({ native }) => {
  // Goose runs a permission-safety classifier turn of its own before it lets a tool run. The judge clears the
  // delegate and the shell call by their call IDs.
  await native.modelScript.rule({
    name: 'the permission judge clears the delegate and shell',
    when: { system: 'permission-safety classifier' },
    respond: { toolCalls: [goosePermissionJudgmentToolCall('judge-goose-live', [LIVE_CHILD_SPAWN_CALL_ID, LIVE_CHILD_SHELL_CALL_ID])] },
  })
  const child = await exerciseLiveChildTranscript(native, {
    childWhen: gooseChildTaskMatcher('Run `echo goose-live`'),
    childTask: 'Run `echo goose-live` and report the result.',
    parentTask: 'Delegate the live shell probe to a child.',
    toolProof: { shell: { command: 'echo goose-live' } },
    childResponse: { text: 'GOOSE_CHILD_LIVE_DONE' },
    // A Goose child sends no text of its own to the child tab.
    finalAnswerInChildTab: false,
  })
  await expectRowBecomesFinal(native.page, child.row)
})
