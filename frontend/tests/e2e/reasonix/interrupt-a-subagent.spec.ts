import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { openRunningNativeChild } from '../helpers/runningChildProof'
import { applyPermissionPreset } from '../helpers/ui'
import { expectUnsupportedSubagent } from '../helpers/unsupportedSubagent'
import { reasonixTest } from '../reasonix-fixtures'

reasonixTest('refuses native child interrupt while the actual child task runs', async ({ authenticatedReasonixWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedReasonixWorkspace.workspaceId, provider: AgentProvider.REASONIX }
  const gate = `native-child-control-${crypto.randomUUID()}`
  const childPrompt = 'Reply with exactly NATIVE_CHILD_CAPABILITY after the operator releases the response.'
  await expectUnsupportedSubagent(context, {
    operation: 'interrupt',
    openChild: () => openRunningNativeChild(context, {
      spawn: spawnSubagentToolCall(AgentProvider.REASONIX, 'native-child-control', { description: 'Hold the native child', prompt: modelScript.prompt(childPrompt) }),
      gate,
      child: { matcher: { user: 'Reply with exactly NATIVE_CHILD_CAPABILITY' } },
      prepare: () => applyPermissionPreset(page, 'bypass'),
    }),
  })
})
