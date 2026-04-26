import { describe, expect, it } from 'vitest'
import {
  claudeMcpFromToolResult,
  formatClaudeMcpDisplayName,
  formatClaudeMcpServerName,
  isClaudeMcpTool,
  parseClaudeMcpToolName,
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

describe('parseClaudeMcpToolName', () => {
  it('splits server and tool', () => {
    expect(parseClaudeMcpToolName('mcp__github__create_issue')).toEqual({
      serverName: 'github',
      toolName: 'create_issue',
    })
  })

  it('preserves further __ segments in the tool name', () => {
    expect(parseClaudeMcpToolName('mcp__github__search__repos')).toEqual({
      serverName: 'github',
      toolName: 'search__repos',
    })
  })

  it('returns null for missing parts', () => {
    expect(parseClaudeMcpToolName('mcp__')).toBeNull()
    expect(parseClaudeMcpToolName('mcp__github__')).toBeNull()
    expect(parseClaudeMcpToolName('Bash')).toBeNull()
  })
})

describe('formatClaudeMcpServerName', () => {
  it('humanizes underscore-separated parts', () => {
    expect(formatClaudeMcpServerName('claude_ai_Tavily')).toBe('Claude Ai Tavily')
  })

  it('handles single words', () => {
    expect(formatClaudeMcpServerName('github')).toBe('Github')
  })
})

describe('formatClaudeMcpDisplayName', () => {
  it('joins server and tool with " / "', () => {
    expect(formatClaudeMcpDisplayName('claude_ai_Tavily', 'tavily_research'))
      .toBe('Claude Ai Tavily / tavily_research')
  })
})

describe('claudeMcpFromToolResult', () => {
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
      server: 'Claude Ai Tavily',
      tool: 'tavily_search',
      content: [{ type: 'text', text: 'A research summary.' }],
      status: 'completed',
    })
    expect(source?.argsJson).toContain('"query"')
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
      { type: 'image', mimeType: 'image/png', urlOrData: undefined },
    ])
    expect(source?.argsJson).toBe('')
  })

  it('marks error and surfaces text content as the error message', () => {
    const source = claudeMcpFromToolResult({
      toolName: 'mcp__github__create_issue',
      resultContent: [{ type: 'text', text: 'Permission denied' }],
      isError: true,
    })
    expect(source?.status).toBe('failed')
    expect(source?.error).toBe('Permission denied')
    expect(source?.content).toEqual([])
  })

  it('omits arguments when toolInput is empty', () => {
    const source = claudeMcpFromToolResult({
      toolName: 'mcp__github__list',
      toolInput: {},
      resultContent: '',
    })
    expect(source?.argsJson).toBe('')
  })
})
