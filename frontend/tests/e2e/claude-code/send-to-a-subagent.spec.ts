import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { claudeTest } from '../claude-fixtures'
import { registerClaudeChildReportRules } from '../helpers/claudeChildReportRule'
import { spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { openRunningNativeChild } from '../helpers/runningChildProof'
import { expectUnsupportedSubagent } from '../helpers/unsupportedSubagent'

claudeTest('refuses a message to a real running child without delivering it to the model', async ({ authenticatedClaudeWorkspace, page, leapmuxServer, modelScript }) => {
  const context = { page, modelScript, leapmuxServer, provider: AgentProvider.CLAUDE_CODE, workspaceId: authenticatedClaudeWorkspace.workspaceId }
  await expectUnsupportedSubagent(context, {
    operation: 'send',
    openChild: () => openRunningNativeChild(context, {
      gate: 'root-child-send-proof',
      childMatcher: { user: 'ROOT_SEND_CHILD_MARKER' },
      spawn: spawnSubagentToolCall(AgentProvider.CLAUDE_CODE, 'spawn-readonly-child', { description: 'root send child', prompt: modelScript.prompt('Wait for ROOT_SEND_CHILD_MARKER.') }),
      beforeRelease: async () => {
        await registerClaudeChildReportRules(modelScript, { spawnCallId: 'spawn-readonly-child', report: 'NATIVE_CHILD_FINAL_REPLY', reply: 'The read-only child report arrived.', completionStatus: 'completed', completionReply: 'The native child completion notification arrived.' })
      },
    }),
  })
  const status = await modelScript.status()
  expect(status.requests.some(request => JSON.stringify(request.body).includes('CHILD_MESSAGE_MUST_NOT_REACH_NATIVE_MODEL'))).toBe(false)
})
