import { describe, expect, it } from 'vitest'
import { claudeWebSearchFromToolResult } from './webSearch'

describe('claudeWebSearchFromToolResult', () => {
  it('returns null when no results array', () => {
    expect(claudeWebSearchFromToolResult(null)).toBeNull()
    expect(claudeWebSearchFromToolResult({})).toBeNull()
  })

  it('extracts links and summary from a results array', () => {
    const source = claudeWebSearchFromToolResult({
      query: 'how to widget',
      durationSeconds: 1.5,
      results: [
        { content: [
          { url: 'https://a.example/page', title: 'A' },
          { url: 'https://b.example/page', title: 'B' },
        ] },
        'Final summary text',
      ],
    })
    expect(source).toEqual({
      links: [
        { url: 'https://a.example/page', title: 'A' },
        { url: 'https://b.example/page', title: 'B' },
      ],
      summary: 'Final summary text',
      query: 'how to widget',
      durationSeconds: 1.5,
    })
  })

  it('deduplicates links by URL', () => {
    const source = claudeWebSearchFromToolResult({
      results: [
        { content: [
          { url: 'https://x.example', title: 'X' },
          { url: 'https://x.example', title: 'X (dup)' },
        ] },
      ],
    })
    expect(source?.links).toEqual([
      { url: 'https://x.example', title: 'X' },
    ])
  })
})
