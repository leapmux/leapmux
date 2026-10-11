import { mkdirSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { expect } from '@playwright/test'
import { kiroExtractControl } from '../../../src/components/chat/providers/kiro/extractControl'
import { KIRO_OPTION, KIRO_POLICY_PRESET } from '../../../src/generated/contracts/kiro-protocol'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { writeMcpEchoServer } from '../helpers/mcpEchoServer'
import { readMcpServerReceipt, waitForMcpToolListed } from '../helpers/mcpServerReceipt'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { runNativeToolTurn } from '../helpers/nativeToolExecution'
import { exerciseMissingWorkspaceTrustRoute, exerciseNativeWorkspaceTrustLimit } from '../helpers/nativeWorkspaceTrustLimit'
import { mcpToolCall } from '../helpers/providerToolCalls'
import { kiroTest } from '../kiro-fixtures'
import { kiroToolInputSchema } from './toolCatalog'
import { kiroToolResult } from './toolResult'

kiroTest('classifies real native controls and proves the missing workspace-trust route', async ({ native }) => {
  await exerciseMissingWorkspaceTrustRoute(native, { askOption: 'policyPreset-ask', classify: kiroExtractControl })
})

kiroTest('loads project MCP configuration without a workspace trust decision', async ({ native }) => {
  await exerciseNativeWorkspaceTrustLimit(native, {
    optionValues: { [KIRO_OPTION.PolicyPreset]: KIRO_POLICY_PRESET.AllowAll },
    projectConfiguration: {
      prepare: ({ directory, marker }) => {
        const server = writeMcpEchoServer(directory, { receiptLog: join(directory, 'native-project-mcp-receipt.json') })
        const config = join(directory, '.kiro', 'settings', 'mcp.json')
        mkdirSync(dirname(config), { recursive: true })
        // The marker is the key of the server, so a tool name that holds the marker can only come from this project file.
        writeFileSync(config, JSON.stringify({ mcpServers: { [marker]: { command: server.command, args: [...server.args] } } }))
      },
      prove: async (privateContext, { directory, marker }) => {
        const request = await sendNativeAnswer(privateContext, 'Reply once after project MCP configuration loads.', 'The project MCP turn completed.')
        const receiptLog = join(directory, 'native-project-mcp-receipt.json')
        await waitForMcpToolListed(receiptLog, 'echo')
        const callId = `native-project-echo-${marker}`
        const value = `NATIVEPROJECTECHO${marker}`
        const echoCall = mcpToolCall(AgentProvider.KIRO, callId, { server: marker.toLowerCase(), tool: 'echo', input: { value } })
        expect(kiroToolInputSchema(request, echoCall.name)).toMatchObject({ type: 'object', properties: { value: { type: 'string' } }, required: ['value'] })
        // The Allow All preset runs the tool with no permission request.
        const { resultRequest } = await runNativeToolTurn(privateContext, {
          toolCalls: [echoCall],
          prompt: 'Call the actual project MCP echo tool once.',
          answer: 'The native project MCP tool completed.',
          permissions: 'none',
        })
        expect(kiroToolResult(resultRequest, callId).text).toContain(`MCP_ECHO:${value}`)
        expect(readMcpServerReceipt(receiptLog).toolResults).toContainEqual(expect.objectContaining({ tool: 'echo', text: `MCP_ECHO:${value}`, isError: false }))
      },
    },
  })
})
