import { existsSync, mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { kiroToolResult } from '../helpers/kiroToolResult'
import { writeMcpEchoServer } from '../helpers/mcpEchoServer'
import { readMcpServerReceipt } from '../helpers/mcpServerReceipt'
import { mcpToolCall } from '../helpers/providerToolCalls'
import { openWorkspace, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { kiroTest, openKiroAgent } from '../kiro-fixtures'

kiroTest('runs the actual project MCP tool and receives its native service result', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  let receiptLog = ''
  await openKiroAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { policyPreset: 'allow-all' }, (workingDir) => {
    receiptLog = join(workingDir, 'native-mcp-receipt.json')
    const script = writeMcpEchoServer(workingDir, { receiptLog })
    const settings = join(workingDir, '.kiro', 'settings')
    mkdirSync(settings, { recursive: true })
    writeFileSync(join(settings, 'mcp.json'), JSON.stringify({ mcpServers: { echo_probe: { command: process.execPath, args: [script] } } }))
  })
  await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
  await expect.poll(() => existsSync(receiptLog) && readMcpServerReceipt(receiptLog).toolCatalogs.some(catalog => catalog.tools.some(tool => tool.name === 'echo'))).toBe(true)
  const callId = 'native-kiro-echo'
  const value = 'KIRO_NATIVE_ECHO_VALUE'
  await modelScript.queue(
    { toolCalls: [mcpToolCall(AgentProvider.KIRO, callId, { server: 'echo_probe', tool: 'echo', input: { value } })] },
    { text: 'The native Kiro MCP tool completed.' },
  )
  await sendMessage(page, modelScript.prompt('Call the registered native echo tool once.'))
  const status = await modelScript.waitForSteps(2)
  await waitForAgentIdle(page)
  const request = status.requests.find(record => record.stepIndex === 1)
  if (!request)
    throw new Error('The Kiro MCP result reached no native model request.')
  expect(kiroToolResult(request, callId).text).toContain(`MCP_ECHO:${value}`)
  expect(readMcpServerReceipt(receiptLog).toolResults).toContainEqual(expect.objectContaining({ tool: 'echo', text: `MCP_ECHO:${value}`, isError: false }))
})
