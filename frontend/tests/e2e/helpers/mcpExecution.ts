import type { Page } from '@playwright/test'
import type { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { ModelScript } from './modelScriptFixture'
import type { NativeToolResultReader } from './nativeScenario'
import { existsSync } from 'node:fs'
import { expect } from '@playwright/test'
import { readMcpServerReceipt } from './mcpServerReceipt'
import { nativeToolResult } from './nativeToolResult'
import { mcpToolCall } from './providerToolCalls'
import { assistantBubbles, sendMessage, waitForAgentIdle } from './ui'

/** Run one native MCP call and check the server's echoed value. */
export async function exerciseMcpEcho(page: Page, modelScript: ModelScript, provider: AgentProvider, value: string, options: { receiptLog?: string, readToolResult?: NativeToolResultReader } = {}): Promise<void> {
  const receiptLog = options.receiptLog
  const callId = `mcp-${value}`
  const answer = `The ${value} MCP echo completed.`
  const start = await modelScript.queue(
    { toolCalls: [mcpToolCall(provider, callId, { server: 'echo_probe', tool: 'echo', input: { value } })] },
    { text: answer },
  )
  await sendMessage(page, modelScript.prompt(`Call echo_probe echo with ${value}.`))
  if (receiptLog) {
    await expect.poll(() => existsSync(receiptLog) && readMcpServerReceipt(receiptLog).toolCatalogs.some(catalog => catalog.tools.some(tool => tool.name === 'echo'))).toBe(true)
  }
  await modelScript.waitForSteps(start + 1)
  const banner = page.getByTestId('control-banner').filter({ visible: true })
  await expect.poll(async () => (await banner.isVisible()) || (await modelScript.status()).nextStep >= start + 2).toBe(true)
  if (await banner.isVisible()) {
    await page.getByTestId('control-allow-btn').filter({ visible: true }).click()
    await expect(banner).toHaveCount(0)
  }

  await modelScript.waitForSteps(start + 2)
  await waitForAgentIdle(page)
  const resultRequest = await modelScript.requestAt(start + 1)
  const result = options.readToolResult
    ? await options.readToolResult(resultRequest, callId)
    : { text: nativeToolResult(resultRequest, callId) }
  expect(result.text).toContain(`MCP_ECHO:${value}`)
  if (options.receiptLog) {
    const receipt = readMcpServerReceipt(options.receiptLog)
    expect(receipt.initializeCapabilities).not.toBeNull()
    expect(receipt.toolResults.some(result => result.tool === 'echo' && result.text === `MCP_ECHO:${value}` && !result.isError)).toBe(true)
    expect(receipt.elicitationRequests).toEqual([])
  }
  await expect(assistantBubbles(page).filter({ hasText: answer }).first()).toBeVisible()
}
