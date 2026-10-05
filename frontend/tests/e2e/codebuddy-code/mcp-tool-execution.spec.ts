import { existsSync, unlinkSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { CODEBUDDY_MODE } from '../../../src/generated/contracts/codebuddy-protocol'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { CODEBUDDY_E2E_SKIP_REASON, codebuddyTest, createCodebuddyWorkingDir, expect, openCodebuddyAgent } from '../codebuddy-fixtures'
import { writeMcpFormServer } from '../helpers/mcpFormServer'
import { codebuddyWaitForMcpServersToolCall, mcpToolCall } from '../helpers/providerToolCalls'
import { messageBubbles, openWorkspace, sendMessage, waitForAgentIdle } from '../helpers/ui'

codebuddyTest.describe('CodeBuddy Code MCP input form', () => {
  codebuddyTest.skip(!!CODEBUDDY_E2E_SKIP_REASON, CODEBUDDY_E2E_SKIP_REASON || '')

  function installUserMcpServer(configDir: string, script: string): () => void {
    const configPath = join(configDir, '.mcp.json')
    if (existsSync(configPath))
      throw new Error('the isolated CodeBuddy user MCP config already exists')
    writeFileSync(configPath, JSON.stringify({ mcpServers: { form_probe: { command: process.execPath, args: [script] } } }))
    return () => unlinkSync(configPath)
  }

  codebuddyTest('executes a disposable MCP tool through the native server', async ({ authenticatedEmptyWorkspace, leapmuxServer, page, modelScript }) => {
    const workingDir = createCodebuddyWorkingDir()
    const echoArguments = { query: 'codebuddy', limit: 0, tail: 'END_MCP_ARGUMENTS' }
    const script = writeMcpFormServer(workingDir, 'form-server.mjs', { expectedEchoArguments: echoArguments })
    const configDir = leapmuxServer.agentEnv.CODEBUDDY_CONFIG_DIR
    if (!configDir)
      throw new Error('the CodeBuddy E2E environment needs an isolated config directory')
    const removeUserMcpServer = installUserMcpServer(configDir, script)
    try {
      await openCodebuddyAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { permissionMode: CODEBUDDY_MODE.BypassPermissions }, workingDir)
      await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
      await modelScript.queue(
        { toolCalls: [codebuddyWaitForMcpServersToolCall('wait-for-echo', ['form_probe'])] },
        { toolCalls: [mcpToolCall(AgentProvider.CODEBUDDY, 'codebuddy-mcp-echo', { server: 'form_probe', tool: 'echo', input: echoArguments })] },
        { text: 'The MCP echo completed.' },
      )
      await sendMessage(page, modelScript.prompt('Wait for form_probe, then call its echo tool.'))
      const status = await modelScript.waitForSteps()
      await waitForAgentIdle(page)
      expect(JSON.stringify(status.requests.find(request => request.stepIndex === 2)?.body)).toContain('PERMISSION_ACCEPTED')
      await expect(messageBubbles(page).filter({ hasText: 'The MCP echo completed.' }).first()).toBeVisible()
    }
    finally {
      removeUserMcpServer()
    }
  })
})
