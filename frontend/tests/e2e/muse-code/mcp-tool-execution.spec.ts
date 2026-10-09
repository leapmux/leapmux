/**
 * The model calls a Model Context Protocol tool through Muse, and its result reaches
 * the next native model request.
 *
 * The run environment registers the echo server in Muse's private settings
 * (`createMuseEnvironment`), so the tool arrives under its own server name.
 */
import { expect } from '@playwright/test'
import { MCP_ECHO_SERVER_NAME } from '../helpers/mcpEchoServer'
import { exerciseMcpEcho } from '../helpers/mcpExecution'
import { runNativeToolTurn } from '../helpers/nativeToolExecution'
import { nativeToolResultContent } from '../helpers/nativeToolResult'
import { mcpToolCall } from '../helpers/providerToolCalls'
import { museTest } from '../muse-fixtures'

museTest('executes a real MCP echo through the native Muse model protocol', async ({ native }) => {
  await exerciseMcpEcho(native, 'muse')
})

museTest('preserves the exact native MCP call identity and result', async ({ native }) => {
  const callId = 'muse-mcp-identity'
  const { resultRequest } = await runNativeToolTurn(native, {
    toolCalls: [mcpToolCall(native.provider, callId, { server: MCP_ECHO_SERVER_NAME, tool: 'echo', input: { value: 'MUSE_MCP_IDENTITY' } })],
    prompt: 'Call the echo server once with the identity value.',
    answer: 'The native MCP echo returned its identity value.',
    permissions: 'none',
  })
  const result = nativeToolResultContent(resultRequest, callId)
  expect(String(result)).toContain('MCP_ECHO:MUSE_MCP_IDENTITY')
})
