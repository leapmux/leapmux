import { Buffer } from 'node:buffer'
import { expect } from '@playwright/test'
import { diracTest } from '../dirac-fixtures'
import { nativeCodeExecutionSchema, openNativeCatalogTurn } from '../helpers/nativeCodeExecution'
import { nativeMessageBody, nativeMessageSupplement, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { runNativeToolTurn } from '../helpers/nativeToolExecution'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { codeExecutionToolCall } from '../helpers/providerToolCalls'
import { uniqueMarker } from '../helpers/shellArguments'
import { openWorkspace, toolCallRow } from '../helpers/ui'
import { diracScriptReceipt } from './codeExecution'
import { nativeContext } from './scenarios'

diracTest('executes native scripts with computed, failed, and empty output after reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }, testInfo) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  const catalog = await openNativeCatalogTurn(context, { directoryPrefix: 'dirac-native-script-' })
  nativeCodeExecutionSchema(catalog, 'execute_command', { script: 'string', language: 'string' })
  const agent = await currentNativeAgent(context)
  const marker = uniqueMarker('DIRACSCRIPT')
  const scripts = [
    { label: 'output', source: `console.log(${JSON.stringify(marker)} + (40 + 2));`, expected: `${marker}42\n`, exitCode: 0 },
    { label: 'failure', source: `console.error(${JSON.stringify(marker)} + (70 + 7)); process.exit(7);`, expected: `${marker}77\n`, exitCode: 7 },
    { label: 'empty', source: 'void 0;', expected: '', exitCode: 0 },
  ]
  for (const item of scripts) {
    if (item.expected)
      expect(item.source).not.toContain(item.expected.trim())
    const call = codeExecutionToolCall(context.provider, `native-dirac-${item.label}`, item.source)
    const { resultRequest: request } = await runNativeToolTurn(context, {
      toolCalls: [call],
      prompt: `Run the native ${item.label} script.`,
      answer: `The native ${item.label} script ended.`,
    })
    if (item.expected)
      expect(nativeToolResult(request, call.id)).toContain(item.expected.trim())
    const readReceipt = async () => {
      const snapshot = await readNativeMessageSnapshot(context, agent.id)
      expect(snapshot.agentSessionId).toBe(agent.agentSessionId)
      const frames = snapshot.messages.map(message => ({ original: nativeMessageBody(message), supplemental: nativeMessageSupplement(message) }))
      await testInfo.attach(`dirac-${item.label}-native-script`, { body: Buffer.from(JSON.stringify({ agentId: agent.id, sessionId: agent.agentSessionId, modelCallId: call.id, source: item.source, frames })), contentType: 'application/json' })
      return diracScriptReceipt(frames, item.source)
    }
    const receipt = await readReceipt()
    const expectedOutput = item.exitCode === 0
      ? 'Command executed successfully (exit code 0).'
      : `Command failed with exit code ${item.exitCode}.`
    expect(receipt.output).toBe(`${expectedOutput}${item.expected ? `\nOutput:\n${item.expected.trimEnd()}` : ''}`)
    expect(receipt.exitCode).toBe(item.exitCode)
    expect(receipt.failed).toBe(item.exitCode !== 0)
    const result = toolCallRow(page, receipt.callId)
    await expect(result).toHaveCount(1)
    await expect(result).toHaveAttribute('data-tool-status', item.exitCode === 0 ? 'completed' : 'failed')
    if (item.expected)
      await expect(result).toContainText(item.expected.trim())
    await page.reload()
    await openWorkspace(page, context.workspaceId)
    expect(await readReceipt()).toEqual(receipt)
    await expect(result).toHaveCount(1)
    await expect(result).toHaveAttribute('data-tool-status', item.exitCode === 0 ? 'completed' : 'failed')
    if (item.expected)
      await expect(result).toContainText(item.expected.trim())
  }
})
