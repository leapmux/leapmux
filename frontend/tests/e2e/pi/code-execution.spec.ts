import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '../../../src/lib/jsonPick'
import { openAgentViaAPI } from '../helpers/api'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { piCodemodeToolCall } from '../helpers/providerToolCalls'
import { getGlobalState } from '../helpers/server'
import { openWorkspace, sendMessage, toolCallRow, waitForAgentIdle } from '../helpers/ui'
import { newProviderWorkingDir } from '../helpers/workspace'
import { piTest } from '../pi-fixtures'
import { activateNativeCodemode } from './codemodeConfiguration'
import { readPiMcpResult } from './mcpResult'
import { verifyPiOutputFilePaths } from './outputFilePaths'
import { nativeContext, PI_AGENT } from './scenarios'
import { withMockPiModel } from './scriptedModel'

piTest('keeps the native codemode output path and preview without an MCP call after reload', async ({ page, context, modelScript, authenticatedEmptyWorkspace, leapmuxServer }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  const directory = newProviderWorkingDir(PI_AGENT, 'pi-native-codemode-full-output-')
  const output = `${Array.from({ length: 3000 }, (_, index) => `codemode-line-${index}`).join('\n')}\nNATIVE_CODEMODE_COMPLETE`
  activateNativeCodemode(directory, getGlobalState().tmpDir)
  await withMockPiModel(directory, leapmuxServer, async (settings) => {
    await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, directory, { agentProvider: AgentProvider.PI, ...settings })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const code = `// @options: {"max_output_tokens": 100}\ntext(${JSON.stringify(output)})`
    const start = await modelScript.queue({ toolCalls: [piCodemodeToolCall('native-codemode-only', code)] }, { text: 'The native codemode-only output completed.' })
    await sendMessage(page, modelScript.prompt('Run the native codemode output probe.'))
    await modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(page)
    const native = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    const completed = await readPiMcpResult(native, 'native-codemode-only', 'codemode')
    expect(completed.failed).toBe(false)
    expect(isObject(completed.result.details) && completed.result.details.calls).toEqual([])
    expect(isObject(completed.result.details) && completed.result.details.fullOutputPath).toMatch(/pi-codemode-[0-9a-f]{16}\.txt$/)
    const bubble = toolCallRow(page, 'native-codemode-only')
    await expect(bubble).toHaveCount(1)
    await expect(bubble).toHaveAttribute('data-tool-status', 'completed')
    await expect(bubble).toContainText('codemode-line-0')
    await expect(bubble).toContainText('codemode-line-2999')
    await expect(bubble).toContainText('Script completed')
    await verifyPiOutputFilePaths(native, { callId: 'native-codemode-only', expectedText: output, omittedMarker: 'codemode-line-1500' })
    await expect(bubble).toContainText('codemode-line-2999')
    await expect(bubble).toContainText('NATIVE_CODEMODE_COMPLETE')
  })
})

piTest('keeps empty native codemode output and a real nested file read after reload', async ({ page, modelScript, authenticatedEmptyWorkspace, leapmuxServer }) => {
  const directory = newProviderWorkingDir(PI_AGENT, 'pi-native-codemode-boundaries-')
  const file = join(directory, 'native-codemode-read.txt')
  const value = `NATIVE_CODEMODE_READ_${crypto.randomUUID()}`
  writeFileSync(file, value)
  activateNativeCodemode(directory, getGlobalState().tmpDir)
  await withMockPiModel(directory, leapmuxServer, async (settings) => {
    const workspaceId = authenticatedEmptyWorkspace.workspaceId
    await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, workspaceId, directory, { agentProvider: AgentProvider.PI, ...settings })
    await openWorkspace(page, workspaceId)
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId })
    const cases = [
      { callId: 'native-codemode-empty', code: 'text("");', expected: '' },
      { callId: 'native-codemode-read', code: `const result = await tools.read({ path: ${JSON.stringify(file)} }); text(result);`, expected: value },
    ]
    for (const [index, item] of cases.entries()) {
      const start = await modelScript.queue({ toolCalls: [piCodemodeToolCall(item.callId, item.code)] }, { text: `The native codemode boundary ${index} completed.` })
      await sendMessage(page, modelScript.prompt(`Run native codemode boundary ${index}.`))
      await modelScript.waitForSteps(start + 2)
      await waitForAgentIdle(page)
      const completed = await readPiMcpResult(context, item.callId, 'codemode')
      expect(completed.failed).toBe(false)
      if (index === 0) {
        expect(isObject(completed.result.details) && completed.result.details.calls).toEqual([])
        const content = completed.result.content
        if (!Array.isArray(content) || !isObject(content[0]) || typeof content[0].text !== 'string')
          throw new Error('The empty native codemode result has no complete status header.')
        expect(content[0].text).toMatch(/^Script completed\nWall time [\d.]+ seconds\nOutput:\n$/)
        expect(content.slice(1).every(block => isObject(block) && block.type === 'text' && block.text === '')).toBe(true)
        if (!isObject(completed.result.details))
          throw new Error('The empty native codemode result has no execution details.')
        expect(completed.result.details.fullOutputPath).toBeUndefined()
      }
      else {
        expect(item.code).not.toContain(value)
        expect(JSON.stringify(completed.result.content)).toContain(value)
        const nested = await readPiMcpResult(context, `${item.callId}/1`, 'read')
        expect(nested.failed).toBe(false)
        expect(nested.result.content).toEqual([{ type: 'text', text: value }])
      }
      const bubble = toolCallRow(page, item.callId)
      await expect(bubble).toHaveCount(1)
      await expect(bubble).toHaveAttribute('data-tool-status', 'completed')
      if (item.expected)
        await expect(bubble).toContainText(item.expected)
      await page.reload()
      await openWorkspace(page, workspaceId)
      await expect(bubble).toHaveCount(1)
      await expect(bubble).toHaveAttribute('data-tool-status', 'completed')
      if (item.expected)
        await expect(bubble).toContainText(item.expected)
    }
  })
})

piTest('keeps the native codemode script failure and exact failed status after reload', async ({ page, modelScript, authenticatedEmptyWorkspace, leapmuxServer }) => {
  const directory = newProviderWorkingDir(PI_AGENT, 'pi-native-codemode-failure-')
  activateNativeCodemode(directory, getGlobalState().tmpDir)
  await withMockPiModel(directory, leapmuxServer, async (settings) => {
    await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, directory, { agentProvider: AgentProvider.PI, ...settings })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const callId = 'native-codemode-failure'
    const start = await modelScript.queue({ toolCalls: [piCodemodeToolCall(callId, 'throw new Error("NATIVE_CODEMODE_FAILURE");')] }, { text: 'The native script failure reached the model.' })
    await sendMessage(page, modelScript.prompt('Run the native codemode error probe.'))
    await modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(page)
    expect(nativeToolResult(await modelScript.requestAt(start + 1), callId)).toContain('NATIVE_CODEMODE_FAILURE')
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    expect((await readPiMcpResult(context, callId, 'codemode')).failed).toBe(true)
    const bubble = toolCallRow(page, callId)
    await expect(bubble).toHaveAttribute('data-tool-status', 'failed')
    await expect(bubble).toContainText('Script failed')
    await expect(bubble).toContainText('NATIVE_CODEMODE_FAILURE')
    await page.reload()
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expect(bubble).toHaveAttribute('data-tool-status', 'failed')
    await expect(bubble).toContainText('NATIVE_CODEMODE_FAILURE')
  })
})
