import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { NATIVE_CHILD_FINAL_REPLY, openRunningNativeChild } from '../helpers/runningChildProof'
import { expectUnsupportedSubagent } from '../helpers/unsupportedSubagent'
import { piTest } from '../pi-fixtures'
import { registerPiChildNoticeRule } from './childNoticeRule'

piTest('refuses native child send while the actual child task runs', async ({ authenticatedPiWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedPiWorkspace.workspaceId, provider: AgentProvider.PI }
  const gate = `native-child-control-${crypto.randomUUID()}`
  const childPrompt = 'Reply with exactly NATIVE_CHILD_CAPABILITY after the operator releases the response.'
  await expectUnsupportedSubagent(context, {
    operation: 'send',
    openChild: () => openRunningNativeChild(context, {
      spawn: spawnSubagentToolCall(AgentProvider.PI, 'native-child-control', { description: 'Hold the native child', prompt: modelScript.prompt(childPrompt) }),
      gate,
      child: { matcher: { user: '^Reply with exactly NATIVE_CHILD_CAPABILITY' } },
      beforeRelease: async () => {
        await registerPiChildNoticeRule(modelScript, { name: 'the actual Pi capability child completed', spawnCallId: 'native-child-control', description: 'Hold the native child', report: NATIVE_CHILD_FINAL_REPLY, reply: 'The native child notification arrived.' })
      },
    }),
  })
})
