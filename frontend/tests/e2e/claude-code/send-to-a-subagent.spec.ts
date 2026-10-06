import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { claudeTest } from '../claude-fixtures'
import { spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { NATIVE_CHILD_FINAL_REPLY, openRunningNativeChild } from '../helpers/runningChildProof'
import { expectUnsupportedSubagent } from '../helpers/unsupportedSubagent'
import { registerClaudeChildReportRules } from './childReportRule'

// `expectUnsupportedSubagent` also requires that no model request carries the refused message after the child ends.
claudeTest('refuses a message to a real running child without delivering it to the model', async ({ native }) => {
  const { modelScript } = native
  await expectUnsupportedSubagent(native, {
    operation: 'send',
    openChild: () => openRunningNativeChild(native, {
      gate: 'root-child-send-proof',
      child: { matcher: { user: 'ROOT_SEND_CHILD_MARKER' } },
      spawn: spawnSubagentToolCall(AgentProvider.CLAUDE_CODE, 'spawn-readonly-child', { description: 'root send child', prompt: modelScript.prompt('Wait for ROOT_SEND_CHILD_MARKER.') }),
      beforeRelease: async () => {
        await registerClaudeChildReportRules(modelScript, { spawnCallId: 'spawn-readonly-child', report: NATIVE_CHILD_FINAL_REPLY, reply: 'The read-only child report arrived.' })
      },
    }),
  })
})
