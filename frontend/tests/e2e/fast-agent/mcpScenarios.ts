import type { NativeMcpToolCall } from '../helpers/mcpExecution'
import type { McpProbeServer } from '../helpers/mcpProbeServer'
import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { existsSync } from 'node:fs'
import { expect } from '@playwright/test'
import { exerciseNativePermissionDecision } from '../helpers/nativePermission'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { mcpToolCall } from '../helpers/providerToolCalls'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'

/**
 * Connect the actual native MCP client to one disposable local server, and wait for `startedFile`.
 * The server writes that file once the client runs it, such as a receipt that the server writes when it starts, so the
 * file shows that the client ran the command.
 */
export async function connectNativeMcp(context: ManagedNativeScenarioContext, server: McpProbeServer, startedFile: string): Promise<void> {
  const command = [server.command, ...server.args].map(argument => JSON.stringify(argument)).join(' ')
  await sendMessage(context.page, `/mcp connect --name ${server.name} ${command}`)
  await waitForAgentIdle(context.page)
  await expect.poll(() => existsSync(startedFile)).toBe(true)
}

/**
 * Allow the native permission request of one MCP call, and return the model request that holds its native result.
 * Fast Agent asks before each MCP tool runs, and its banner states the tool.
 */
export async function invokeNativeMcp(context: ManagedNativeScenarioContext, call: NativeMcpToolCall): Promise<MockModelRequestRecord> {
  return exerciseNativePermissionDecision(context, {
    toolCall: mcpToolCall(context.provider, call.callId, { server: call.server, tool: call.tool, input: call.input }),
    decision: 'allow',
    beforeDecision: banner => expect(banner).toContainText(call.tool),
    // The reader fails unless the request holds exactly one result for the call.
    nativeProof: (request) => {
      nativeToolResult(request, call.callId)
    },
  })
}
