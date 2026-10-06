import type { McpProbeServer } from './mcpProbeServer'
import type { MockModelRequestRecord } from './mockModelScript'
import type { ManagedNativeScenarioContext, NativeScenarioContext } from './nativeScenario'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { MCP_ECHO_SERVER_NAME } from './mcpEchoServer'
import { writeMcpFormServer } from './mcpFormServer'
import { readMcpServerReceipt, waitForMcpToolListed } from './mcpServerReceipt'
import { newNativeWorkingDir, openNativeAgent } from './nativeAgentOpen'
import { withNativeConfigurationFile } from './nativeConfigurationFile'
import { nativeToolOutcome } from './nativeScenario'
import { runNativeToolTurn } from './nativeToolExecution'
import { mcpToolCall } from './providerToolCalls'
import { getGlobalState } from './server'
import { applyPermissionPreset, assistantBubbles } from './ui'

/** One native MCP tool call: the server and tool that the agent calls, the call ID, and the arguments. */
export interface NativeMcpToolCall {
  server: string
  tool: string
  callId: string
  input: Record<string, unknown>
}

/** The answer that the model gives after the MCP call `callId`. Each call ID gives its own answer. */
export function nativeMcpAnswer(callId: string): string {
  return `The MCP call ${callId} completed.`
}

/**
 * Run one native turn that calls an MCP tool, and return the model request that holds the tool result.
 * The turn allows each native approval through `runNativeToolTurn`, and it measures its steps from the index that
 * `queue` returns, so an earlier turn of the test cannot move them. The model then answers {@link nativeMcpAnswer}.
 */
export async function invokeNativeMcpTool(context: NativeScenarioContext, call: NativeMcpToolCall): Promise<MockModelRequestRecord> {
  if (call.callId.trim() === '')
    throw new Error('A native MCP call needs a call ID.')
  const toolCall = mcpToolCall(context.provider, call.callId, { server: call.server, tool: call.tool, input: call.input })
  // The caller reads the result under `call.callId`. A provider whose builder changes the ID (Droid adds `call_`) would
  // put the result under another ID, so the call refuses that provider before the turn.
  if (toolCall.id !== call.callId)
    throw new Error(`The MCP call of provider ${context.provider} has the ID ${toolCall.id}, not ${call.callId}. Read its result through the built call.`)
  const { resultRequest } = await runNativeToolTurn(context, {
    toolCalls: [toolCall],
    prompt: `Call the ${call.server} ${call.tool} tool once.`,
    answer: nativeMcpAnswer(call.callId),
  })
  return resultRequest
}

/**
 * Run one native MCP call of the echo server, and check the echoed value in the tool result that the model reads.
 * With `receiptLog`, the helper also reads the receipt of the echo server: the catalog that the agent listed, and an
 * echo result of `value`. Without it, the helper checks only the tool result that the model reads.
 */
export async function exerciseMcpEcho(context: NativeScenarioContext, value: string, options: { receiptLog?: string } = {}): Promise<void> {
  const callId = `mcp-${value}`
  const request = await invokeNativeMcpTool(context, { server: MCP_ECHO_SERVER_NAME, tool: 'echo', callId, input: { value } })
  expect((await nativeToolOutcome(context, request, callId)).text).toContain(`MCP_ECHO:${value}`)
  if (options.receiptLog) {
    await waitForMcpToolListed(options.receiptLog, 'echo')
    const receipt = readMcpServerReceipt(options.receiptLog)
    expect(receipt.initializeCapabilities).not.toBeNull()
    expect(receipt.toolResults.some(result => result.tool === 'echo' && result.text === `MCP_ECHO:${value}` && !result.isError)).toBe(true)
    expect(receipt.elicitationRequests).toEqual([])
  }
  await expect(assistantBubbles(context.page).filter({ hasText: nativeMcpAnswer(callId) }).first()).toBeVisible()
}

/** Where a provider reads its MCP servers, and how the file registers one server. */
export interface NativeMcpFormAgentOptions {
  /**
   * The prefix of the new working directory of the agent, which follows the rule of the provider of the context. The
   * probe server and its receipt live there too.
   */
  directoryPrefix: string
  /** The native MCP configuration file, which the agent reads when it starts. */
  configurationPath: string
  /** The value of that file with `server` registered. The file holds its JSON text. */
  configuration: (server: McpProbeServer) => unknown
}

/** The probe form server of {@link withNativeMcpFormAgent}, and the receipt that it writes. */
export interface NativeMcpFormProbe {
  server: McpProbeServer
  receiptLog: string
}

/**
 * Register the probe form server for an agent of `context.provider`, and run `use`:
 *
 * - Write the server and its receipt into a new working directory, which follows the rule of the provider of the
 *   context (`newNativeWorkingDir`).
 * - Register the server in the native MCP configuration.
 * - Open the agent in that directory with the bypass preset.
 *
 * The configuration returns to its exact earlier bytes after `use` ends, whether `use` passes or fails. An agent can
 * start its servers later than its open. So `use` must wait for the catalog before it calls a tool, unless the agent
 * starts the server for the call.
 */
export async function withNativeMcpFormAgent(
  context: ManagedNativeScenarioContext,
  options: NativeMcpFormAgentOptions,
  use: (probe: NativeMcpFormProbe) => Promise<void>,
): Promise<void> {
  const workingDir = newNativeWorkingDir(context, options.directoryPrefix)
  const receiptLog = join(workingDir, 'native-mcp-receipt.json')
  const server = writeMcpFormServer(workingDir, 'native-form-server.mjs', { receiptLog })
  const content = JSON.stringify(options.configuration(server))
  await withNativeConfigurationFile({ path: options.configurationPath, content, runDir: getGlobalState().tmpDir }, async () => {
    await openNativeAgent(context, { workingDir })
    await applyPermissionPreset(context.page, 'bypass')
    await use({ server, receiptLog })
  })
}
