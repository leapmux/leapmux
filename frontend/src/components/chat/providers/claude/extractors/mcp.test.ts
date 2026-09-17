import { describe, expect, it } from 'vitest'
import {
  claudeMcpFromToolResult,
  isClaudeMcpTool,
} from './mcp'

describe('isClaudeMcpTool', () => {
  it('matches mcp__server__tool', () => {
    expect(isClaudeMcpTool('mcp__github__create_issue')).toBe(true)
  })

  it('rejects non-MCP tool names', () => {
    expect(isClaudeMcpTool('Bash')).toBe(false)
    expect(isClaudeMcpTool('mcp_server')).toBe(false)
    expect(isClaudeMcpTool('')).toBe(false)
  })
})

describe('claudeMcpFromToolResult', () => {
  it('keeps the same server identifier that other providers render', () => {
    expect(claudeMcpFromToolResult({ toolName: 'mcp__render_probe__echo', resultContent: 'Result' })?.server).toBe('render_probe')
  })

  it('returns null for non-MCP tool names', () => {
    expect(claudeMcpFromToolResult({
      toolName: 'Bash',
      resultContent: 'output',
    })).toBeNull()
  })

  it('extracts a completed MCP call (string content)', () => {
    const source = claudeMcpFromToolResult({
      toolName: 'mcp__claude_ai_Tavily__tavily_search',
      toolInput: { query: 'react hooks' },
      resultContent: 'A research summary.',
    })
    expect(source).toMatchObject({
      server: 'claude_ai_Tavily',
      tool: 'tavily_search',
      content: [{ type: 'text', text: 'A research summary.' }],
    })
  })

  it('parses Claude content arrays into structured items', () => {
    const source = claudeMcpFromToolResult({
      toolName: 'mcp__github__search__repos',
      toolInput: {},
      resultContent: [
        { type: 'text', text: '## Results' },
        { type: 'image', mimeType: 'image/png' },
      ],
    })
    expect(source?.content).toEqual([
      { type: 'text', text: '## Results' },
      { type: 'image', source: { mimeType: 'image/png' } },
    ])
    expect(source?.argsJson).toBeUndefined()
  })

  it('marks error and surfaces text content as the error message', () => {
    const source = claudeMcpFromToolResult({
      toolName: 'mcp__github__create_issue',
      resultContent: [{ type: 'text', text: 'Permission denied' }],
      isError: true,
    })
    expect(source?.failed).toBe(true)
    expect(source?.error).toBe('Permission denied')
    expect(source?.content).toEqual([])
  })

  it('omits arguments when toolInput is empty', () => {
    const source = claudeMcpFromToolResult({
      toolName: 'mcp__github__list',
      toolInput: {},
      resultContent: '',
    })
    expect(source?.argsJson).toBeUndefined()
  })
})
