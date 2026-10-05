import { Buffer } from 'node:buffer'
import { existsSync, unlinkSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { CODEBUDDY_MODE } from '../../../src/generated/contracts/codebuddy-protocol'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '../../../src/lib/jsonPick'
import { CODEBUDDY_E2E_SKIP_REASON, codebuddyTest, createCodebuddyWorkingDir, expect, openCodebuddyAgent } from '../codebuddy-fixtures'
import { writeMcpFormServer } from '../helpers/mcpFormServer'
import { nativeMcpRefusal, readMcpServerReceipt } from '../helpers/mcpServerReceipt'
import { nativeMessageBody, nativeMessageSupplement, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { codebuddyWaitForMcpServersToolCall, mcpToolCall } from '../helpers/providerToolCalls'
import { openWorkspace, sendMessage, waitForAgentIdle } from '../helpers/ui'

codebuddyTest.describe('CodeBuddy Code MCP input form', () => {
  codebuddyTest.skip(!!CODEBUDDY_E2E_SKIP_REASON, CODEBUDDY_E2E_SKIP_REASON || '')

  function installUserMcpServer(configDir: string, script: string): () => void {
    const configPath = join(configDir, '.mcp.json')
    if (existsSync(configPath))
      throw new Error('The isolated CodeBuddy user MCP config already exists.')
    writeFileSync(configPath, JSON.stringify({ mcpServers: { form_probe: { command: process.execPath, args: [script] } } }))
    return () => unlinkSync(configPath)
  }

  codebuddyTest('shows no form when stream JSON declines native MCP elicitation', async ({ authenticatedEmptyWorkspace, leapmuxServer, page, modelScript }, testInfo) => {
    const workingDir = createCodebuddyWorkingDir()
    const receiptLog = join(workingDir, 'mcp-input-receipt.jsonl')
    const script = writeMcpFormServer(workingDir, 'form-server.mjs', { receiptLog })
    const configDir = leapmuxServer.agentEnv.CODEBUDDY_CONFIG_DIR
    if (!configDir)
      throw new Error('The CodeBuddy end-to-end environment requires an isolated config directory.')
    const removeUserMcpServer = installUserMcpServer(configDir, script)
    try {
      await openCodebuddyAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { permissionMode: CODEBUDDY_MODE.BypassPermissions }, workingDir)
      await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)

      const step = (await modelScript.status()).stepCount
      const call = mcpToolCall(AgentProvider.CODEBUDDY, 'codebuddy-mcp-form', { server: 'form_probe', tool: 'ask', input: {} })
      await modelScript.queue(
        { toolCalls: [codebuddyWaitForMcpServersToolCall('wait-for-form', ['form_probe'])] },
        { toolCalls: [call] },
        { text: 'The native client refused the form request.' },
      )
      await sendMessage(page, modelScript.prompt('Wait for form_probe, then call its ask tool.'))
      const status = await modelScript.waitForSteps(step + 3)
      await waitForAgentIdle(page)

      const second = status.requests.find(request => request.stepIndex === step + 1)?.body
      if (!second || typeof second !== 'object' || !('tools' in second))
        throw new Error('CodeBuddy must call the model after its MCP server connects')
      expect((JSON.stringify(second.tools) ?? '').includes('mcp__form_probe__ask')).toBe(true)

      const resultRequest = status.requests.find(request => request.stepIndex === step + 2)
      const refusal = nativeMcpRefusal(readMcpServerReceipt(receiptLog))
      expect(refusal.reply.error).toMatchObject({ code: -32601, message: 'Method not found' })
      expect(nativeToolResult(resultRequest, call.id)).toBe(refusal.toolResult.text)
      const nativeContext = { page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId, provider: AgentProvider.CODEBUDDY }
      const agent = await currentNativeAgent(nativeContext)
      const snapshot = await readNativeMessageSnapshot(nativeContext, agent.id)
      const frames = snapshot.messages.map(message => ({ id: message.id, spanId: message.spanId, spanType: message.spanType, completion: message.completion, frame: nativeMessageBody(message), supplement: nativeMessageSupplement(message) }))
        .filter(({ frame }) => {
          if (!isObject(frame))
            return false
          if (frame.callId === call.id)
            return true
          const message = isObject(frame.message) ? frame.message : undefined
          return Array.isArray(message?.content) && message.content.some(block => isObject(block)
            && ((block.type === 'tool_use' && block.id === call.id) || (block.type === 'tool_result' && block.tool_use_id === call.id)))
        })
      expect(frames.length).toBeGreaterThan(0)
      await testInfo.attach('codebuddy-mcp-refusal-worker-snapshot', { body: Buffer.from(JSON.stringify({ agentId: snapshot.agentId, agentSessionId: snapshot.agentSessionId, frames })), contentType: 'application/json' })
      const nativeResults = frames.flatMap(({ frame }) => {
        const message = isObject(frame) && isObject(frame.message) ? frame.message : undefined
        return Array.isArray(message?.content) ? message.content.filter(isObject).filter(block => block.type === 'tool_result' && block.tool_use_id === call.id) : []
      })
      expect(nativeResults).toHaveLength(1)
      expect(nativeResults[0]?.is_error).toBe(false)

      await expect(page.getByTestId('elicitation-form').filter({ visible: true })).toHaveCount(0)
      await expect(page.getByTestId('control-banner').filter({ visible: true })).toHaveCount(0)
      const result = page.locator(`[data-testid="message-bubble"][data-tool-call-id="${call.id}"][data-tool-row-role="result"]:visible`)
      await expect(result).toHaveCount(1)
      // CodeBuddy's native stream reports is_error:false for this refused form result.
      await expect(result).toHaveAttribute('data-tool-status', 'completed')
      await expect(result).toContainText(refusal.toolResult.text)
      await page.reload()
      await expect(result).toHaveCount(1)
      await expect(result).toHaveAttribute('data-tool-status', 'completed')
      await expect(result).toContainText(refusal.toolResult.text)
      await expect(page.getByTestId('elicitation-form').filter({ visible: true })).toHaveCount(0)
      await expect(page.getByTestId('control-banner').filter({ visible: true })).toHaveCount(0)
    }
    finally {
      removeUserMcpServer()
    }
  })
})
