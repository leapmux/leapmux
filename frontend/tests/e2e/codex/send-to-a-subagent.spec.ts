import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codexTest } from '../codex-fixtures'
import { spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { openRunningNativeChild } from '../helpers/runningChildProof'
import { expectUnsupportedSubagent } from '../helpers/unsupportedSubagent'

// `expectUnsupportedSubagent` also requires that no model request carries the refused message after the child ends.
codexTest('refuses a message to a real running child without delivering it to the model', async ({ native }) => {
  await expectUnsupportedSubagent(native, {
    operation: 'send',
    openChild: () => openRunningNativeChild(native, {
      gate: 'root-child-send-proof',
      child: { matcher: { body: ['NEW_TASK', 'root_send_child'] } },
      spawn: spawnSubagentToolCall(AgentProvider.CODEX, 'spawn-readonly-child', { description: 'root send child', prompt: native.modelScript.prompt('Wait for ROOT_SEND_CHILD_MARKER.') }),
    }),
  })
})
