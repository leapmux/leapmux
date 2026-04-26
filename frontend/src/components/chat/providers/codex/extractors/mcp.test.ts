import { describe, expect, it } from 'vitest'
import { codexMcpFromItem } from './mcp'

describe('codexMcpFromItem', () => {
  it('returns null for null/undefined', () => {
    expect(codexMcpFromItem(null)).toBeNull()
    expect(codexMcpFromItem(undefined)).toBeNull()
  })

  it('returns null for non-MCP item types', () => {
    expect(codexMcpFromItem({ type: 'agentMessage' })).toBeNull()
    expect(codexMcpFromItem({ type: 'commandExecution' })).toBeNull()
  })

  describe('mcpToolCall', () => {
    it('extracts a completed call', () => {
      const source = codexMcpFromItem({
        type: 'mcpToolCall',
        server: 'tavily',
        tool: 'tavily_search',
        status: 'completed',
        arguments: { query: 'rust' },
        result: {
          content: [
            { type: 'text', text: 'Result A' },
            { type: 'text', text: 'Result B' },
          ],
          structuredContent: { count: 2 },
        },
        durationMs: 350,
      })
      expect(source).toMatchObject({
        server: 'tavily',
        tool: 'tavily_search',
        status: 'completed',
        durationMs: 350,
        content: [
          { type: 'text', text: 'Result A' },
          { type: 'text', text: 'Result B' },
        ],
      })
      expect(source?.argsJson).toContain('"query"')
      expect(source?.structuredJson).toContain('"count"')
    })

    it('extracts a failed call with error message', () => {
      const source = codexMcpFromItem({
        type: 'mcpToolCall',
        server: 'gh',
        tool: 'create_issue',
        status: 'failed',
        arguments: {},
        result: null,
        error: { message: 'Permission denied' },
      })
      expect(source?.status).toBe('failed')
      expect(source?.error).toBe('Permission denied')
      expect(source?.content).toEqual([])
    })

    it('treats missing status as inProgress', () => {
      const source = codexMcpFromItem({
        type: 'mcpToolCall',
        server: 'tavily',
        tool: 'tavily_search',
        arguments: { query: 'rust' },
      })
      expect(source?.status).toBe('inProgress')
      expect(source?.content).toEqual([])
    })

    it('skips structuredContent when null/empty', () => {
      const source = codexMcpFromItem({
        type: 'mcpToolCall',
        server: 'a',
        tool: 'b',
        status: 'completed',
        result: { content: [], structuredContent: null },
      })
      expect(source?.structuredJson).toBeUndefined()
    })
  })

  describe('dynamicToolCall', () => {
    it('extracts inputText content items', () => {
      const source = codexMcpFromItem({
        type: 'dynamicToolCall',
        namespace: 'openai',
        tool: 'browser',
        status: 'completed',
        arguments: { url: 'https://example.com' },
        contentItems: [
          { type: 'inputText', text: 'Hello dynamic' },
        ],
      })
      expect(source).toMatchObject({
        server: 'openai',
        tool: 'browser',
        status: 'completed',
        content: [{ type: 'text', text: 'Hello dynamic' }],
      })
    })

    it('maps inputImage to an image content item', () => {
      const source = codexMcpFromItem({
        type: 'dynamicToolCall',
        namespace: '',
        tool: 'screenshot',
        status: 'completed',
        contentItems: [{ type: 'inputImage', imageUrl: 'https://example.com/x.png' }],
      })
      expect(source?.content).toEqual([
        { type: 'image', urlOrData: 'https://example.com/x.png' },
      ])
    })

    it('falls back to "Tool call failed" when failed with no content', () => {
      const source = codexMcpFromItem({
        type: 'dynamicToolCall',
        namespace: '',
        tool: 'flaky',
        status: 'failed',
        contentItems: [],
      })
      expect(source?.error).toBe('Tool call failed')
    })

    it('does not synthesize an error when failed with content items', () => {
      const source = codexMcpFromItem({
        type: 'dynamicToolCall',
        namespace: '',
        tool: 'flaky',
        status: 'failed',
        contentItems: [{ type: 'inputText', text: 'real error message' }],
      })
      expect(source?.error).toBeUndefined()
    })
  })
})
