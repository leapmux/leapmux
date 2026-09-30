import type { Page } from '@playwright/test'
import type { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { ModelScript } from './modelScriptFixture'
import { expect } from '@playwright/test'
import { mcpToolCall } from './providerToolCalls'
import { assistantBubbles, sendMessage, waitForAgentIdle } from './ui'

/** Run one native MCP call and check the server's echoed value. */
export async function exerciseMcpEcho(page: Page, modelScript: ModelScript, provider: AgentProvider, value: string): Promise<void> {
  const answer = `The ${value} MCP echo completed.`
  await modelScript.queue(
    { toolCalls: [mcpToolCall(provider, `mcp-${value}`, { server: 'echo_probe', tool: 'echo', input: { value } })] },
    { text: answer },
  )
  await sendMessage(page, modelScript.prompt(`Call echo_probe echo with ${value}.`))
  await modelScript.waitForSteps(1)
  const banner = page.getByTestId('control-banner').filter({ visible: true })
  await expect.poll(async () => (await banner.isVisible()) || (await modelScript.status()).nextStep >= 2).toBe(true)
  if (await banner.isVisible()) {
    await page.getByTestId('control-allow-btn').filter({ visible: true }).click()
    await expect(banner).toHaveCount(0)
  }

  const status = await modelScript.waitForSteps()
  await waitForAgentIdle(page)
  const resultRequest = status.requests.find(request => request.stepIndex === 1)
  expect(JSON.stringify(resultRequest?.body)).toContain(`MCP_ECHO:${value}`)
  await expect(assistantBubbles(page).filter({ hasText: answer }).first()).toBeVisible()
}
