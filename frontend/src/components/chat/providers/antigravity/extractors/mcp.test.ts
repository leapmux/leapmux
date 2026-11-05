import { describe, expect, it } from 'vitest'
import { antigravityMcpRequest, antigravityMcpResult } from './mcp'
import { antigravityStep } from './toolCommon'

describe('antigravity MCP extraction', () => {
  it('shows the server and tool from the native MCP payload', () => {
    const step = antigravityStep({
      type: 'CORTEX_STEP_TYPE_MCP_TOOL',
      metadata: {
        toolCall: { name: 'call_mcp_tool', argumentsJson: '{"ServerName":"ignored","ToolName":"ignored"}' },
      },
      mcpTool: {
        serverName: 'probe',
        toolCall: { id: 'call', name: 'inspect', argumentsJson: '{"count":0}' },
        resultString: 'NATIVE_RESULT',
        images: [
          { base64Data: 'aGVsbG8=', mimeType: 'image/png' },
        ],
      },
    })!
    expect(antigravityMcpRequest(step)).toEqual({
      server: 'probe',
      tool: 'inspect',
      args: { ServerName: 'ignored', ToolName: 'ignored' },
    })
    expect(antigravityMcpResult(step).content).toEqual([
      { type: 'text', text: 'NATIVE_RESULT' },
      {
        type: 'image',
        source: expect.objectContaining({ data: 'aGVsbG8=' }),
      },
    ])
  })
})
