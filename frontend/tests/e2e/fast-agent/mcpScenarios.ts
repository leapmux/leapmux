import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { nativeTextStep } from '../helpers/nativeScenario'
import { mcpToolCall } from '../helpers/providerToolCalls'
import { sendMessage, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'

/** Connect the actual native MCP client to one disposable local server. */
export async function connectNativeMcp(context: ManagedNativeScenarioContext, name: string, command: string, script: string): Promise<void> {
  await sendMessage(context.page, `/mcp connect --name ${name} ${JSON.stringify(command)} ${JSON.stringify(script)}`)
  await waitForAgentIdle(context.page)
}

/** Approve the real MCP tool and return the request that contains its native result. */
export async function invokeNativeMcp(
  context: ManagedNativeScenarioContext,
  options: { server: string, tool: string, input: Record<string, unknown>, callId: string },
): Promise<MockModelRequestRecord> {
  const start = (await context.modelScript.status()).stepCount
  await context.modelScript.queue(
    { toolCalls: [mcpToolCall(context.provider, options.callId, options)] },
    nativeTextStep(context, 'The native MCP call completed.'),
  )
  await sendMessage(context.page, context.modelScript.prompt(`Call the configured ${options.server} ${options.tool} tool once.`))
  await context.modelScript.waitForSteps(start + 1)
  const banner = await waitForControlBanner(context.page)
  await expect(banner).toContainText(options.tool)
  await context.page.locator('[data-testid="control-allow-btn"]:visible').click()
  const status = await context.modelScript.waitForSteps(start + 2)
  await waitForAgentIdle(context.page)
  const request = status.requests.find(record => record.stepIndex === start + 1)
  if (!request)
    throw new Error('The native MCP result reached no next model request.')
  return request
}
