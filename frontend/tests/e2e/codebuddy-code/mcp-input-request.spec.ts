import { Buffer } from 'node:buffer'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { isObject } from '../../../src/lib/jsonPick'
import { CODEBUDDY_BYPASS, codebuddyTest } from '../codebuddy-fixtures'
import { writeMcpFormServer } from '../helpers/mcpFormServer'
import { nativeMcpRefusal, readMcpServerReceipt } from '../helpers/mcpServerReceipt'
import { nativeMessageBody, nativeMessageSupplement, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent, nativeTextStep } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { codebuddyWaitForMcpServersToolCall, mcpToolCall } from '../helpers/providerToolCalls'
import { newProviderWorkingDir } from '../helpers/providerWorkingDir'
import { expectNoControlBanner, openWorkspace, sendMessage, toolCallRow, waitForAgentIdle } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { withCodebuddyUserMcpServer } from './mcpConfiguration'
import { CODEBUDDY_AGENT, nativeContext } from './scenarios'

codebuddyTest.describe('CodeBuddy Code MCP input form', () => {
  codebuddyTest('shows no form when stream JSON declines native MCP elicitation', async ({ authenticatedEmptyWorkspace, leapmuxServer, page, modelScript }, testInfo) => {
    const workingDir = newProviderWorkingDir(CODEBUDDY_AGENT)
    const receiptLog = join(workingDir, 'mcp-input-receipt.jsonl')
    const server = writeMcpFormServer(workingDir, 'form-server.mjs', { receiptLog })
    await withCodebuddyUserMcpServer(leapmuxServer.agentEnv, server, async () => {
      await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, CODEBUDDY_AGENT, { ...CODEBUDDY_BYPASS, workingDir })
      await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)

      const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
      const call = mcpToolCall(context.provider, 'codebuddy-mcp-form', { server: server.name, tool: 'ask', input: {} })
      const start = await modelScript.queue(
        { toolCalls: [codebuddyWaitForMcpServersToolCall('wait-for-form', [server.name])] },
        { toolCalls: [call] },
        nativeTextStep(context, 'The native client refused the form request.'),
      )
      await sendMessage(page, modelScript.prompt(`Wait for ${server.name}, then call its ask tool.`))
      await modelScript.waitForSteps(start + 3)
      await waitForAgentIdle(page)

      const second = (await modelScript.requestAt(start + 1)).body
      if (!second || typeof second !== 'object' || !('tools' in second))
        throw new Error('CodeBuddy must call the model after its MCP server connects')
      expect((JSON.stringify(second.tools) ?? '').includes(`mcp__${server.name}__ask`)).toBe(true)

      const resultRequest = await modelScript.requestAt(start + 2)
      const refusal = nativeMcpRefusal(readMcpServerReceipt(receiptLog))
      expect(refusal.reply.error).toMatchObject({ code: -32601, message: 'Method not found' })
      expect(nativeToolResult(resultRequest, call.id)).toBe(refusal.toolResult.text)
      const agent = await currentNativeAgent(context)
      const snapshot = await readNativeMessageSnapshot(context, agent.id)
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
      await expectNoControlBanner(page)
      const result = toolCallRow(page, call.id)
      await expect(result).toHaveCount(1)
      // CodeBuddy's native stream reports is_error:false for this refused form result.
      await expect(result).toHaveAttribute('data-tool-status', 'completed')
      await expect(result).toContainText(refusal.toolResult.text)
      await page.reload()
      await expect(result).toHaveCount(1)
      await expect(result).toHaveAttribute('data-tool-status', 'completed')
      await expect(result).toContainText(refusal.toolResult.text)
      await expect(page.getByTestId('elicitation-form').filter({ visible: true })).toHaveCount(0)
      await expectNoControlBanner(page)
    })
  })
})
