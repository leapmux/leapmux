import { describe, expect, it } from 'vitest'
import { antigravityStep } from './toolCommon'
import { antigravityWebSearchRequest, antigravityWebSearchResult } from './webSearch'

function makeSearchStep(
  rawOverrides: Record<string, unknown> = {},
  argumentsJson?: string,
) {
  const step = antigravityStep({
    type: 'CORTEX_STEP_TYPE_SEARCH_WEB',
    status: 'CORTEX_STEP_STATUS_DONE',
    metadata: {
      toolCall: {
        id: 'call-search',
        name: 'search_web',
        ...(argumentsJson !== undefined
          ? { argumentsJson }
          : {}),
      },
    },
    ...rawOverrides,
  })
  if (!step)
    throw new Error('Expected a valid Antigravity search step.')
  return step
}

describe('antigravityWebSearchRequest', () => {
  it('prefers the query from tool arguments over searchWeb.query', () => {
    const step = makeSearchStep(
      {
        searchWeb: { query: 'from step' },
      },
      JSON.stringify({ query: 'from args' }),
    )
    expect(antigravityWebSearchRequest(step)).toEqual({ query: 'from args' })
  })

  it('falls back to searchWeb.query when tool arguments omit query, pass empty query, or are absent', () => {
    const stepWithoutQueryArg = makeSearchStep(
      {
        searchWeb: { query: 'from step' },
      },
      JSON.stringify({ domain: 'example.com' }),
    )
    const stepWithEmptyQueryArg = makeSearchStep(
      {
        searchWeb: { query: 'from step' },
      },
      JSON.stringify({ query: '' }),
    )
    const stepWithoutArgs = makeSearchStep({
      searchWeb: { query: 'from step' },
    })

    expect(antigravityWebSearchRequest(stepWithoutQueryArg)).toEqual({ query: 'from step' })
    expect(antigravityWebSearchRequest(stepWithEmptyQueryArg)).toEqual({ query: 'from step' })
    expect(antigravityWebSearchRequest(stepWithoutArgs)).toEqual({ query: 'from step' })
  })

  it('returns an empty query when neither tool arguments nor searchWeb state one', () => {
    const step = makeSearchStep({})
    expect(antigravityWebSearchRequest(step)).toEqual({ query: '' })
  })

  it('trims whitespace-only tool argument query before falling back to searchWeb.query', () => {
    const stepWithBlankArg = makeSearchStep(
      {
        searchWeb: { query: '  from step  ' },
      },
      JSON.stringify({ query: '   ' }),
    )
    const stepWithBothBlank = makeSearchStep(
      {
        searchWeb: { query: '\t\n ' },
      },
      JSON.stringify({ query: '   ' }),
    )

    expect(antigravityWebSearchRequest(stepWithBlankArg)).toEqual({ query: 'from step' })
    expect(antigravityWebSearchRequest(stepWithBothBlank)).toEqual({ query: '' })
  })
})

describe('antigravityWebSearchResult', () => {
  it('deduplicates urls in order, keeps shared titles on distinct urls, falls back to url, and trims text', () => {
    const step = makeSearchStep({
      searchWeb: {
        summary: '  Trimmed **summary** text. \n ',
        webDocuments: [
          {
            documentId: 'doc-1',
            title: '  csdn.net  ',
            url: '  https://example.com/a  ',
          },
          {
            documentId: 'doc-dup',
            title: 'Duplicate ignored',
            url: 'https://example.com/a',
          },
          {
            documentId: 'doc-2',
            title: 'csdn.net',
            url: 'https://example.com/b',
          },
          {
            documentId: 'doc-blank-title',
            title: '   ',
            url: 'https://example.com/c',
          },
          {
            documentId: 'doc-missing-title',
            url: 'https://example.com/d',
          },
        ],
      },
    })

    expect(antigravityWebSearchResult(step)).toEqual({
      links: [
        { title: 'csdn.net', url: 'https://example.com/a' },
        { title: 'csdn.net', url: 'https://example.com/b' },
        { title: 'https://example.com/c', url: 'https://example.com/c' },
        { title: 'https://example.com/d', url: 'https://example.com/d' },
      ],
      summary: 'Trimmed **summary** text.',
    })
  })

  it.each([
    undefined,
    null,
    'invalid',
    {},
  ])('handles absent or malformed searchWeb payload %j without throwing', (searchWeb) => {
    const step = makeSearchStep(
      searchWeb === undefined
        ? {}
        : { searchWeb },
    )
    expect(antigravityWebSearchResult(step)).toEqual({
      links: [],
      summary: '',
    })
  })

  it.each([
    undefined,
    null,
    'invalid',
    {},
  ])('handles invalid webDocuments value %j without throwing', (webDocuments) => {
    const step = makeSearchStep({
      searchWeb: {
        summary: '   \n\t  ',
        webDocuments,
      },
    })
    expect(antigravityWebSearchResult(step)).toEqual({
      links: [],
      summary: '',
    })
  })

  it('skips non-object items and entries with missing or blank urls while keeping valid links', () => {
    const step = makeSearchStep({
      searchWeb: {
        summary: '   ',
        webDocuments: [
          null,
          0,
          'text',
          { title: 'No URL' },
          { title: 'Blank URL', url: '   ' },
          { title: 'Valid', url: 'https://example.com/valid' },
        ],
      },
    })

    expect(antigravityWebSearchResult(step)).toEqual({
      links: [
        { title: 'Valid', url: 'https://example.com/valid' },
      ],
      summary: '',
    })
  })
})
