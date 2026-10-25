import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { MCP_FORM_SERVER_NAME } from '../helpers/mcpFormServer'
import { exerciseMcpProbeFormRoundTrip } from '../helpers/mcpProbeForm'
import { nativeModelToolNames } from '../helpers/nativeScenario'
import { mcpToolCall } from '../helpers/providerToolCalls'
import { answerControl, waitForControlBanner } from '../helpers/ui'

/**
 * Answer the form of the probe server through the browser, and return the model request that holds the tool result.
 * Qoder asks before the MCP tool runs, and its banner states the native name of the tool.
 *
 * Qoder 1.1.78 serves a form only for a dynamic server, so the Worker registers the configured probe server again
 * (`registerMcpServersForForms` in the Qoder provider). The dynamic entry replaces the configured one, so the model
 * must still see each tool of the server exactly once.
 */
export async function exerciseQoderMcpForm(context: ManagedNativeScenarioContext): Promise<MockModelRequestRecord> {
  const callId = 'qoder-mcp-form'
  const toolName = mcpToolCall(context.provider, callId, { server: MCP_FORM_SERVER_NAME, tool: 'ask', input: {} }).name
  const request = await exerciseMcpProbeFormRoundTrip(context, {
    callId,
    reloadBeforeSubmit: false,
    approveTool: async () => {
      await expect(await waitForControlBanner(context.page)).toContainText(toolName)
      await answerControl(context.page, 'allow')
    },
  })
  expect(nativeModelToolNames(request).filter(name => name === toolName), 'the model sees the tool of the registered server once').toEqual([toolName])
  return request
}
