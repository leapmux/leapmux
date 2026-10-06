import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { grokTest } from '../grok-fixtures'
import { readMcpServerReceipt } from '../helpers/mcpServerReceipt'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { mcpToolCall } from '../helpers/providerToolCalls'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'
import { openGrokMcpWorkspace } from './mcpWorkspace'

grokTest('runs the actual registered MCP tool and reads only its native result', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId, provider: AgentProvider.GROK_BUILD }
  const { receiptLog } = await openGrokMcpWorkspace(context, 'allow')
  const callId = 'native-grok-echo'
  const value = 'GROK_NATIVE_ECHO_VALUE'
  await modelScript.queue(
    { toolCalls: [mcpToolCall(AgentProvider.GROK_BUILD, callId, { server: 'echo_probe', tool: 'echo', input: { value } })] },
    { text: 'The registered native MCP tool completed.' },
  )
  await sendMessage(page, modelScript.prompt('Call the registered native echo tool once.'))
  const status = await modelScript.waitForSteps(2)
  await waitForAgentIdle(page)
  expect(nativeToolResult(status.requests.find(request => request.stepIndex === 1), callId)).toContain(`MCP_ECHO:${value}`)
  const receipt = readMcpServerReceipt(receiptLog)
  expect(receipt.toolResults).toContainEqual(expect.objectContaining({ tool: 'echo', text: `MCP_ECHO:${value}`, isError: false }))
})
