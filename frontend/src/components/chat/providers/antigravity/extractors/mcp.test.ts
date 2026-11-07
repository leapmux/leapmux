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

describe('antigravityMcpResult', () => {
  it.each([undefined, null, 'invalid', {}])('keeps text when the native image collection is absent or invalid: %j', (images) => {
    const step = antigravityStep({
      type: 'CORTEX_STEP_TYPE_MCP_TOOL',
      mcpTool: { resultString: 'native text', images },
    })!
    expect(antigravityMcpResult(step).content).toEqual([
      { type: 'text', text: 'native text' },
    ])
  })

  it('filters non-object image entries and preserves image order and file URI hosts', () => {
    const step = antigravityStep({
      type: 'CORTEX_STEP_TYPE_MCP_TOOL',
      mcpTool: {
        images: [
          null,
          0,
          'invalid',
          { base64Data: 'aGVsbG8=', mimeType: 'image/png', uri: 'file://server/share/first%20image.png' },
          { base64Data: 'd29ybGQ=', mimeType: 'image/jpeg', uri: 'file:///second.jpg' },
        ],
      },
    })!
    expect(antigravityMcpResult(step).content).toEqual([
      { type: 'text', text: '' },
      {
        type: 'image',
        source: expect.objectContaining({ data: 'aGVsbG8=', filePath: '//server/share/first image.png' }),
      },
      {
        type: 'image',
        source: expect.objectContaining({ data: 'd29ybGQ=', filePath: '/second.jpg' }),
      },
    ])
  })
})
