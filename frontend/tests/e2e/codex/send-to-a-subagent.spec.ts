import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codexTest } from '../codex-fixtures'
import { spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { openRunningNativeChild } from '../helpers/runningChildProof'
import { expectUnsupportedSubagent } from '../helpers/unsupportedSubagent'

codexTest('refuses a message to a real running child without delivering it to the model', async ({ authenticatedCodexWorkspace, page, leapmuxServer, modelScript }) => {
  const context = { page, modelScript, leapmuxServer, provider: AgentProvider.CODEX, workspaceId: authenticatedCodexWorkspace.workspaceId }
  await expectUnsupportedSubagent(context, {
    operation: 'send',
    openChild: () => openRunningNativeChild(context, {
      gate: 'root-child-send-proof',
      childMatcher: { body: ['NEW_TASK', 'root_send_child'] },
      spawn: spawnSubagentToolCall(AgentProvider.CODEX, 'spawn-readonly-child', { description: 'root send child', prompt: modelScript.prompt('Wait for ROOT_SEND_CHILD_MARKER.') }),
    }),
  })
  const status = await modelScript.status()
  expect(status.requests.some(request => JSON.stringify(request.body).includes('CHILD_MESSAGE_MUST_NOT_REACH_NATIVE_MODEL'))).toBe(false)
})
