import { existsSync } from 'node:fs'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { GROK_E2E_SKIP_REASON, grokTest } from '../grok-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { nativeModelToolNames } from '../helpers/nativeScenario'
import { mcpToolCall } from '../helpers/providerToolCalls'
import { openGrokMcpWorkspace } from './mcpWorkspace'

grokTest.skip(!!GROK_E2E_SKIP_REASON, GROK_E2E_SKIP_REASON || '')

for (const decision of ['allow', 'deny'] as const) {
  grokTest(`keeps project configuration unloaded until the native trust decision is ${decision}`, async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
    const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId, provider: AgentProvider.GROK_BUILD }
    const { receiptLog } = await openGrokMcpWorkspace(context, decision)
    const request = await sendNativeAnswer(context, 'Reply once with the current native project configuration.', 'The native project configuration choice reached this turn.')
    const names = nativeModelToolNames(request)
    const echoName = mcpToolCall(AgentProvider.GROK_BUILD, 'catalog-only-echo', { server: 'echo_probe', tool: 'echo', input: { value: 'unused' } }).name
    if (decision === 'allow') {
      expect(existsSync(receiptLog)).toBe(true)
      expect(names).toContain(echoName)
    }
    else {
      expect(existsSync(receiptLog)).toBe(false)
      expect(names).not.toContain(echoName)
    }
  })
}
