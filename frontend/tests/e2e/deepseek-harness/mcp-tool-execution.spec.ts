import { join } from 'node:path'
import { expect } from '@playwright/test'
import { isObject, pickObject } from '../../../src/lib/jsonPick'
import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { invokeNativeMcpTool } from '../helpers/mcpExecution'
import { writeMcpResultServer } from '../helpers/mcpResultServer'
import { readMcpCallExchange } from '../helpers/mcpServerReceipt'
import { nativeCodeExecutionSchema } from '../helpers/nativeCodeExecution'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { runNativeToolTurn } from '../helpers/nativeToolExecution'
import { computedNativeToolOutput, copyNativeToolOutputPreview } from '../helpers/nativeToolOutput'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { deepseekHarnessRunCodeToolCall } from '../helpers/providerToolCalls'
import { toolCallRow } from '../helpers/ui'
import { newProviderWorkingDir } from '../helpers/workspace'
import { withDeepseekHarnessMcp } from './mcpScenarios'
import { deepseekHarnessCanonicalMcpProjection, deepseekHarnessMcpResultMessage } from './mcpToolResult'
import { deepseekHarnessToolResultText } from './nativeToolResultText'
import { DEEPSEEK_HARNESS_AGENT, nativeContext } from './scenarios'

deepseekHarnessTest('uses real native MCP results and preserves failure state after reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const directory = newProviderWorkingDir(DEEPSEEK_HARNESS_AGENT, 'deepseek-mcp-results-')
  const server = writeMcpResultServer(directory, { receiptLog: join(directory, 'receipts.json') })
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  await withDeepseekHarnessMcp(context, { server, workingDir: directory }, async (privateContext) => {
    const inspect = await invokeNativeMcpTool(privateContext, { server: server.name, tool: 'inspect', callId: 'native-inspect', input: { count: 0, enabled: false, text: 'Exact native MCP argument' } })
    expect(deepseekHarnessToolResultText(inspect, 'native-inspect')).toBe('NATIVE_MCP_INSPECT:{"count":0,"enabled":false,"text":"Exact native MCP argument"}')
    const failed = await invokeNativeMcpTool(privateContext, { server: server.name, tool: 'fail', callId: 'native-failure', input: {} })
    expect(nativeToolResult(failed, 'native-failure')).toContain('NATIVE_MCP_FAILED_RESULT')
    const bubble = toolCallRow(page, 'native-failure')
    await expect(bubble).toHaveAttribute('data-tool-status', 'failed')
    await expect(bubble).toContainText('NATIVE_MCP_FAILED_RESULT')
    await page.reload()
    await expect(bubble).toHaveAttribute('data-tool-status', 'failed')
    await expect(bubble).toContainText('NATIVE_MCP_FAILED_RESULT')
  })
})

deepseekHarnessTest('preserves the computed canonical native MCP result while removing private MCP metadata', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const directory = newProviderWorkingDir(DEEPSEEK_HARNESS_AGENT, 'deepseek-mcp-canonical-')
  const receiptLog = join(directory, 'receipts.json')
  const server = writeMcpResultServer(directory, { receiptLog, includeNullable: true })
  const output = computedNativeToolOutput({ lineCount: 8000, padding: 30 })
  const input = { count: 0, enabled: false, text: output.text }
  const native = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  // The native standard preset offers no run_code tool. The native PTC preset offers run_code and keeps the MCP bindings.
  await withDeepseekHarnessMcp(native, { server, workingDir: directory, agentPreset: 'ptc' }, async (privateContext) => {
    const callId = 'native-canonical-mcp-result'
    // The native binding of an MCP tool has the same name as its direct tool: `mcp__<server>__<tool>`.
    const source = `${output.source}
const args = {count: 0, enabled: false, text: completeOutput};
const value = await tools.mcp__${server.name}__inspect(args);
const echoed = JSON.parse(value.content[0].text.slice('NATIVE_MCP_INSPECT:'.length));
return JSON.stringify({contentMatches: value.content.length === 1 && value.content[0].type === 'text' && value.content[0].text === 'NATIVE_MCP_INSPECT:' + JSON.stringify(args), echoedCount: echoed.count, nextCount: value.structuredContent.nextCount, enabled: value.structuredContent.enabled, textMatches: value.structuredContent.text === args.text, textCharacters: value.structuredContent.text.length, nullable: value.structuredContent.nullable, hasNullable: Object.prototype.hasOwnProperty.call(value.structuredContent, 'nullable'), hasPrivateMeta: Object.prototype.hasOwnProperty.call(value, '_meta')});`
    const { toolRequest } = await runNativeToolTurn(privateContext, {
      toolCalls: [deepseekHarnessRunCodeToolCall(callId, source)],
      prompt: 'Read the actual MCP value once through the native code binding.',
      answer: 'The native MCP projection ended.',
    })
    nativeCodeExecutionSchema(toolRequest, 'run_code', { code: 'string', description: 'string' })
    const agent = await currentNativeAgent(privateContext)
    for (const reloaded of [false, true]) {
      if (reloaded)
        await page.reload()
      const snapshot = await readNativeMessageSnapshot(privateContext, agent.id)
      const row = deepseekHarnessMcpResultMessage(snapshot, callId)
      const value = deepseekHarnessCanonicalMcpProjection(nativeMessageBody(row), callId)
      expect(value).toEqual({ contentMatches: true, echoedCount: 0, nextCount: 1, enabled: false, textMatches: true, textCharacters: output.text.length, nullable: null, hasNullable: true, hasPrivateMeta: false })
      expect(value).not.toHaveProperty('_meta')
      const body = nativeMessageBody(row)
      const message = isObject(body) ? pickObject(pickObject(body, 'data'), 'message') : undefined
      const block = Array.isArray(message?.content) && message.content.length === 1 ? message.content[0] : undefined
      if (!isObject(block) || typeof block.text !== 'string')
        throw new Error('The actual MCP projection has no exact native inline text.')
      const bubble = toolCallRow(page, callId)
      await expect(bubble).toHaveCount(1)
      await expect(bubble).toHaveAttribute('data-tool-status', 'completed')
      // The compact projection is one short line. The view shows it in full and offers no Expand control.
      for (const marker of ['"echoedCount":0', '"enabled":false', '"nullable":null', '"hasPrivateMeta":false'])
        await expect(bubble).toContainText(marker)
      await copyNativeToolOutputPreview(page, bubble, block.text)
    }
    const call = readMcpCallExchange(receiptLog)
    expect({ name: call.name, arguments: call.arguments }).toEqual({ name: 'inspect', arguments: input })
    expect(call.result).toMatchObject({ _meta: { privateFixture: true }, structuredContent: { nextCount: 1, enabled: false, text: output.text, nullable: null } })
  })
})
