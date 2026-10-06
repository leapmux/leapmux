import { existsSync, mkdirSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import process from 'node:process'
import { expect } from '@playwright/test'
import { kiroExtractControl } from '../../../src/components/chat/providers/kiro/extractControl'
import { KIRO_OPTION, KIRO_POLICY_PRESET } from '../../../src/generated/contracts/kiro-protocol'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '../../../src/lib/jsonPick'
import { kiroToolResult } from '../helpers/kiroToolResult'
import { writeMcpEchoServer } from '../helpers/mcpEchoServer'
import { readMcpServerReceipt } from '../helpers/mcpServerReceipt'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { createNativePermissionFileWrite, exerciseNativePermissionDecision } from '../helpers/nativePermission'

import { exerciseNativeWorkspaceTrustLimit } from '../helpers/nativeWorkspaceTrustLimit'
import { mcpToolCall } from '../helpers/providerToolCalls'
import { chooseSettingsOption, sendMessage, waitForAgentIdle, waitForSettingsIdle } from '../helpers/ui'
import { exerciseUnsupportedNativeControl } from '../helpers/unsupportedNativeControl'
import { kiroTest } from '../kiro-fixtures'

// LeapMux exposes no interactive native workspace-trust route for this provider.
kiroTest('classifies real native controls and proves the missing workspace-trust route', async ({ page, modelScript, leapmuxServer, authenticatedKiroWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedKiroWorkspace.workspaceId, provider: AgentProvider.KIRO, readToolResult: kiroToolResult }
  await chooseSettingsOption(page, 'policyPreset-ask')
  await waitForSettingsIdle(page)
  const operation = await createNativePermissionFileWrite(context, { fileName: 'native-workspace-trust-control.txt', callId: 'native-workspace-trust-permission', outputPrefix: 'NATIVECONTROL' })
  await exerciseUnsupportedNativeControl(context, {
    purpose: 'workspace-trust',
    classify: kiroExtractControl,
    relatedProof: beforeDecision => exerciseNativePermissionDecision(context, {
      toolCall: operation.toolCall,
      decision: 'allow',
      beforeDecision: async (banner) => {
        await operation.beforeDecision()
        await beforeDecision(banner)
      },
      nativeProof: operation.nativeProof,
    }),
  })
})

kiroTest('loads project MCP configuration without a workspace trust decision', async ({ page, modelScript, leapmuxServer, authenticatedKiroWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedKiroWorkspace.workspaceId, provider: AgentProvider.KIRO }
  await exerciseNativeWorkspaceTrustLimit(context, {
    optionValues: { [KIRO_OPTION.PolicyPreset]: KIRO_POLICY_PRESET.AllowAll },
    projectConfiguration: {
      prepare: ({ directory, marker }) => {
        const script = writeMcpEchoServer(directory, { receiptLog: join(directory, 'native-project-mcp-receipt.json') })
        const config = join(directory, '.kiro', 'settings', 'mcp.json')
        mkdirSync(dirname(config), { recursive: true })
        writeFileSync(config, JSON.stringify({ mcpServers: { [marker]: { command: process.execPath, args: [script] } } }))
      },
      prove: async (privateContext, { directory, marker }) => {
        const request = await sendNativeAnswer(privateContext, 'Reply once after project MCP configuration loads.', 'The project MCP turn completed.')
        const receiptLog = join(directory, 'native-project-mcp-receipt.json')
        await expect.poll(() => existsSync(receiptLog) && readMcpServerReceipt(receiptLog).toolCatalogs.some(catalog => catalog.tools.some(tool => tool.name === 'echo'))).toBe(true)
        expect(readMcpServerReceipt(receiptLog).initializeCapabilities).not.toBeNull()
        const body = isObject(request.body) ? request.body : undefined
        const state = isObject(body?.conversationState) ? body.conversationState : undefined
        const current = isObject(state?.currentMessage) ? state.currentMessage : undefined
        const user = isObject(current?.userInputMessage) ? current.userInputMessage : undefined
        const nativeContext = isObject(user?.userInputMessageContext) ? user.userInputMessageContext : undefined
        const tools = Array.isArray(nativeContext?.tools) ? nativeContext.tools.filter(isObject) : []
        expect(tools.length).toBeGreaterThan(0)
        const nativeTool = tools.find(tool => isObject(tool.toolSpecification) && tool.toolSpecification.name === `mcp_${marker.toLowerCase()}_echo`)
        expect(nativeTool).toBeDefined()
        const specification = isObject(nativeTool?.toolSpecification) ? nativeTool.toolSpecification : undefined
        const inputSchema = isObject(specification?.inputSchema) ? specification.inputSchema : undefined
        expect(inputSchema?.json).toMatchObject({ type: 'object', properties: { value: { type: 'string' } }, required: ['value'] })
        const callId = `native-project-echo-${marker}`
        const value = `NATIVEPROJECTECHO${marker}`
        const start = (await modelScript.status()).stepCount
        await modelScript.queue(
          { toolCalls: [mcpToolCall(AgentProvider.KIRO, callId, { server: marker.toLowerCase(), tool: 'echo', input: { value } })] },
          { text: 'The native project MCP tool completed.' },
        )
        await sendMessage(page, modelScript.prompt('Call the actual project MCP echo tool once.'))
        const status = await modelScript.waitForSteps(start + 2)
        await waitForAgentIdle(page)
        const result = status.requests.find(record => record.stepIndex === start + 1)
        if (!result)
          throw new Error('The project MCP result reached no native Kiro model request.')
        expect(kiroToolResult(result, callId).text).toContain(`MCP_ECHO:${value}`)
        expect(readMcpServerReceipt(receiptLog).toolResults).toContainEqual(expect.objectContaining({ tool: 'echo', text: `MCP_ECHO:${value}`, isError: false }))
      },
    },
  })
})
