import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { openRunningNativeChild } from '../helpers/runningChildProof'
import { expectUnsupportedSubagent } from '../helpers/unsupportedSubagent'
import { kiloTest } from '../kilo-fixtures'

kiloTest('refuses native child interrupt while the actual child task runs', async ({ authenticatedKiloWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedKiloWorkspace.workspaceId, provider: AgentProvider.KILO }
  const gate = `native-child-control-${crypto.randomUUID()}`
  const childPrompt = 'Reply with exactly NATIVE_CHILD_CAPABILITY after the operator releases the response.'
  await expectUnsupportedSubagent(context, {
    operation: 'interrupt',
    openChild: () => openRunningNativeChild(context, {
      spawn: spawnSubagentToolCall(AgentProvider.KILO, 'native-child-control', { description: 'Hold the native child', prompt: modelScript.prompt(childPrompt) }),
      gate,
      childMatcher: { user: '^Reply with exactly NATIVE_CHILD_CAPABILITY' },
    }),
  })
})
