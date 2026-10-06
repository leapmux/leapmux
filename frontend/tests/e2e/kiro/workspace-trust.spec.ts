import { mkdirSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import process from 'node:process'
import { expect } from '@playwright/test'
import { kiroExtractControl } from '../../../src/components/chat/providers/kiro/extractControl'
import { KIRO_OPTION, KIRO_POLICY_PRESET } from '../../../src/generated/contracts/kiro-protocol'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '../../../src/lib/jsonPick'
import { kiroToolResult } from '../helpers/kiroToolResult'
import { writeMcpEchoServer } from '../helpers/mcpEchoServer'
import { readMcpServerReceipt, waitForMcpToolListed } from '../helpers/mcpServerReceipt'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { exerciseMissingWorkspaceTrustRoute, exerciseNativeWorkspaceTrustLimit } from '../helpers/nativeWorkspaceTrustLimit'
import { mcpToolCall } from '../helpers/providerToolCalls'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'
import { kiroTest } from '../kiro-fixtures'

kiroTest('classifies real native controls and proves the missing workspace-trust route', async ({ native }) => {
  await exerciseMissingWorkspaceTrustRoute(native, { askOption: 'policyPreset-ask', classify: kiroExtractControl })
})

kiroTest('loads project MCP configuration without a workspace trust decision', async ({ native, page, modelScript }) => {
  await exerciseNativeWorkspaceTrustLimit(native, {
    optionValues: { [KIRO_OPTION.PolicyPreset]: KIRO_POLICY_PRESET.AllowAll },
    projectConfiguration: {
      prepare: ({ directory, marker }) => {
        const { script } = writeMcpEchoServer(directory, { receiptLog: join(directory, 'native-project-mcp-receipt.json') })
        const config = join(directory, '.kiro', 'settings', 'mcp.json')
        mkdirSync(dirname(config), { recursive: true })
        writeFileSync(config, JSON.stringify({ mcpServers: { [marker]: { command: process.execPath, args: [script] } } }))
      },
      prove: async (privateContext, { directory, marker }) => {
        const request = await sendNativeAnswer(privateContext, 'Reply once after project MCP configuration loads.', 'The project MCP turn completed.')
        const receiptLog = join(directory, 'native-project-mcp-receipt.json')
        await waitForMcpToolListed(receiptLog, 'echo')
        const body = isObject(request.body) ? request.body : undefined
        const state = isObject(body?.conversationState) ? body.conversationState : undefined
        const current = isObject(state?.currentMessage) ? state.currentMessage : undefined
        const user = isObject(current?.userInputMessage) ? current.userInputMessage : undefined
        const inputContext = isObject(user?.userInputMessageContext) ? user.userInputMessageContext : undefined
        const tools = Array.isArray(inputContext?.tools) ? inputContext.tools.filter(isObject) : []
        expect(tools.length).toBeGreaterThan(0)
        const nativeTool = tools.find(tool => isObject(tool.toolSpecification) && tool.toolSpecification.name === `mcp_${marker.toLowerCase()}_echo`)
        expect(nativeTool).toBeDefined()
        const specification = isObject(nativeTool?.toolSpecification) ? nativeTool.toolSpecification : undefined
        const inputSchema = isObject(specification?.inputSchema) ? specification.inputSchema : undefined
        expect(inputSchema?.json).toMatchObject({ type: 'object', properties: { value: { type: 'string' } }, required: ['value'] })
        const callId = `native-project-echo-${marker}`
        const value = `NATIVEPROJECTECHO${marker}`
        const start = await modelScript.queue(
          { toolCalls: [mcpToolCall(AgentProvider.KIRO, callId, { server: marker.toLowerCase(), tool: 'echo', input: { value } })] },
          { text: 'The native project MCP tool completed.' },
        )
        await sendMessage(page, modelScript.prompt('Call the actual project MCP echo tool once.'))
        await modelScript.waitForSteps(start + 2)
        await waitForAgentIdle(page)
        expect(kiroToolResult(await modelScript.requestAt(start + 1), callId).text).toContain(`MCP_ECHO:${value}`)
        expect(readMcpServerReceipt(receiptLog).toolResults).toContainEqual(expect.objectContaining({ tool: 'echo', text: `MCP_ECHO:${value}`, isError: false }))
      },
    },
  })
})
