import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { gooseTest } from '../goose-fixtures'
import { spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { openRunningNativeChild } from '../helpers/runningChildProof'
import { applyPermissionPreset } from '../helpers/ui'
import { expectUnsupportedSubagent } from '../helpers/unsupportedSubagent'

gooseTest('refuses native child interrupt while the actual child task runs', async ({ authenticatedGooseWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedGooseWorkspace.workspaceId, provider: AgentProvider.GOOSE }
  const gate = `native-child-control-${crypto.randomUUID()}`
  const childPrompt = 'Reply with exactly NATIVE_CHILD_CAPABILITY after the operator releases the response.'
  await expectUnsupportedSubagent(context, {
    operation: 'interrupt',
    openChild: () => openRunningNativeChild(context, {
      spawn: spawnSubagentToolCall(AgentProvider.GOOSE, 'native-child-control', { description: 'Hold the native child', prompt: modelScript.prompt(childPrompt) }),
      gate,
      child: { matcher: { user: '^(?:Subagent ID: [^\\n]*\\n+)?Reply with exactly NATIVE_CHILD_CAPABILITY' } },
      prepare: () => applyPermissionPreset(page, 'bypass'),
    }),
  })
})
