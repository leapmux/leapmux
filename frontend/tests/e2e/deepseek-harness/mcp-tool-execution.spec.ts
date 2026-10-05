import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { isObject, pickObject } from '../../../src/lib/jsonPick'
import { test } from '../fixtures'
import { readMcpCallArguments } from '../helpers/mcpRequestReceipt'
import { writeMcpResultServer } from '../helpers/mcpResultServer'
import { nativeCodeExecutionSchema } from '../helpers/nativeCodeExecution'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent, nativeTextStep } from '../helpers/nativeScenario'
import { waitForNativeToolSteps } from '../helpers/nativeToolExecution'
import { computedNativeToolOutput, copyNativeToolOutputPreview } from '../helpers/nativeToolOutput'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { deepseekHarnessRunCodeToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'
import { invokeDeepseekHarnessMcp, withDeepseekHarnessMcp } from './mcpScenarios'
import { deepseekHarnessCanonicalMcpProjection, deepseekHarnessInspectReply, deepseekHarnessMcpResultMessage } from './mcpToolResult'
import { deepseekHarnessToolResultText } from './nativeToolResultText'
import { nativeContext } from './scenarios'

test('uses real native MCP results and preserves failure state after reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const directory = createTestDirectory('deepseek-mcp-results-')
  const script = writeMcpResultServer(directory, { receiptLog: join(directory, 'receipts.json') })
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  await withDeepseekHarnessMcp(context, { name: 'results', script, workingDir: directory }, async (privateContext) => {
    const inspect = await invokeDeepseekHarnessMcp(privateContext, { server: 'results', tool: 'inspect', callId: 'native-inspect', input: { count: 0, enabled: false, text: 'Exact native MCP argument' } })
    expect(deepseekHarnessToolResultText(inspect, 'native-inspect')).toBe('NATIVE_MCP_INSPECT:{"count":0,"enabled":false,"text":"Exact native MCP argument"}')
    const failed = await invokeDeepseekHarnessMcp(privateContext, { server: 'results', tool: 'fail', callId: 'native-failure', input: {} })
    expect(nativeToolResult(failed, 'native-failure')).toContain('NATIVE_MCP_FAILED_RESULT')
    const bubble = page.locator('[data-testid="message-bubble"][data-tool-call-id="native-failure"][data-tool-row-role="result"]:visible')
    await expect(bubble).toHaveAttribute('data-tool-status', 'failed')
    await expect(bubble).toContainText('NATIVE_MCP_FAILED_RESULT')
    await page.reload()
    await expect(bubble).toHaveAttribute('data-tool-status', 'failed')
    await expect(bubble).toContainText('NATIVE_MCP_FAILED_RESULT')
  })
})

test('preserves the computed canonical native MCP result while removing private MCP metadata', async ({ authenticatedEmptyWorkspace, page, context, modelScript, leapmuxServer }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  const directory = createTestDirectory('deepseek-mcp-canonical-')
  const receiptLog = join(directory, 'receipts.json')
  const script = writeMcpResultServer(directory, { receiptLog, inspectNullable: null })
  const output = computedNativeToolOutput({ lineCount: 8000, padding: 30 })
  const input = { count: 0, enabled: false, text: output.text }
  const native = nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  // The native standard preset offers no run_code tool. The native PTC preset offers run_code and keeps the MCP bindings.
  await withDeepseekHarnessMcp(native, { name: 'results', script, workingDir: directory, agentPreset: 'ptc' }, async (privateContext) => {
    const callId = 'native-canonical-mcp-result'
    const source = `${output.source}
const args = {count: 0, enabled: false, text: completeOutput};
const value = await tools.mcp__results__inspect(args);
const echoed = JSON.parse(value.content[0].text.slice('NATIVE_MCP_INSPECT:'.length));
return JSON.stringify({contentMatches: value.content.length === 1 && value.content[0].type === 'text' && value.content[0].text === 'NATIVE_MCP_INSPECT:' + JSON.stringify(args), echoedCount: echoed.count, nextCount: value.structuredContent.nextCount, enabled: value.structuredContent.enabled, textMatches: value.structuredContent.text === args.text, textCharacters: value.structuredContent.text.length, nullable: value.structuredContent.nullable, hasNullable: Object.prototype.hasOwnProperty.call(value.structuredContent, 'nullable'), hasPrivateMeta: Object.prototype.hasOwnProperty.call(value, '_meta')});`
    const start = (await modelScript.status()).stepCount
    await modelScript.queue({ toolCalls: [deepseekHarnessRunCodeToolCall(callId, source)] }, nativeTextStep(privateContext, 'The native MCP projection ended.'))
    await sendMessage(page, modelScript.prompt('Read the actual MCP value once through the native code binding.'))
    await waitForNativeToolSteps(privateContext, start + 2)
    await waitForAgentIdle(page)
    const catalog = (await modelScript.status()).requests.find(request => request.stepIndex === start)
    if (!catalog)
      throw new Error('The native MCP projection has no exact tool catalog request.')
    nativeCodeExecutionSchema(catalog, 'run_code', { code: 'string', description: 'string' })
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
      const bubble = page.locator(`[data-testid="message-bubble"][data-tool-call-id="${callId}"][data-tool-row-role="result"]:visible`)
      await expect(bubble).toHaveCount(1)
      await expect(bubble).toHaveAttribute('data-tool-status', 'completed')
      // The compact projection is one short line. The view shows it in full and offers no Expand control.
      for (const marker of ['"echoedCount":0', '"enabled":false', '"nullable":null', '"hasPrivateMeta":false'])
        await expect(bubble).toContainText(marker)
      await copyNativeToolOutputPreview(page, bubble, block.text)
    }
    expect(readMcpCallArguments(receiptLog)).toEqual([{ name: 'inspect', arguments: input }])
    const receipts: unknown = JSON.parse(readFileSync(receiptLog, 'utf8'))
    expect(deepseekHarnessInspectReply(receipts, input)).toMatchObject({ _meta: { privateFixture: true }, structuredContent: { nextCount: 1, enabled: false, text: output.text, nullable: null } })
  })
})
