import { existsSync } from 'node:fs'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { grokTest } from '../grok-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { nativeModelInstructionText, nativeModelToolNames } from '../helpers/nativeScenario'
import { mcpToolCall } from '../helpers/providerToolCalls'
import { openGrokMcpWorkspace } from './mcpWorkspace'
import { nativeContext } from './scenarios'

for (const decision of ['allow', 'deny'] as const) {
  grokTest(`keeps project configuration unloaded until the native trust decision is ${decision}`, async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    const { receiptLog } = await openGrokMcpWorkspace(context, decision)
    const request = await sendNativeAnswer(context, 'Reply once with the current native project configuration.', 'The native project configuration choice reached this turn.')
    // Grok reaches every MCP server through its two generic tools, `search_tool` and `use_tool`.
    // Its tool catalog therefore holds the dispatcher under both decisions and never names a server tool.
    const dispatcher = mcpToolCall(AgentProvider.GROK_BUILD, 'catalog-only-echo', { server: 'echo_probe', tool: 'echo', input: { value: 'unused' } }).name
    expect(nativeModelToolNames(request)).toContain(dispatcher)
    // A server that loaded is announced to the model, in a system reminder of the prompt that follows its connection.
    const announcements = nativeModelInstructionText(request)
    if (decision === 'allow') {
      expect(existsSync(receiptLog)).toBe(true)
      expect(announcements).toContain('MCP server connected')
      expect(announcements).toContain('echo_probe')
    }
    else {
      expect(existsSync(receiptLog)).toBe(false)
      expect(announcements).not.toContain('echo_probe')
    }
  })
}
