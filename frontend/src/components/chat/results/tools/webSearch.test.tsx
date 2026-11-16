import { render } from '@solidjs/testing-library'
import { beforeAll, describe, expect, it } from 'vitest'
import { checkKindModule } from '~/test-support/kindTestHarness'
import { toolCallFixture, toolRow } from '~/test-support/toolCallFixture'
import { ToolMessage } from '../ToolMessage'
import { toolCallMeta } from './meta'

// jsdom does not provide ResizeObserver, which the shared layouts observe with.
beforeAll(() => {
  globalThis.ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver
})

describe('web_search renderer', () => {
  checkKindModule({
    kind: 'web_search',
    request: {
      query: 'solidjs docs',
      queries: ['solidjs docs', 'solidjs guide'],
    },
    minimalTitlePart: 'q',
    titlePart: 'solidjs docs',
    result: {
      links: [
        { title: 'Docs', url: 'https://solidjs.com' },
      ],
      summary: 'found',
    },
    resultPart: 'Docs',
  })
})

// One search runs several queries, and the title states the first. The queries are
// arguments of the call, so the row that draws the request lists the rest on Expand,
// and the result row lists only what the search found.
describe('web_search queries', () => {
  const request = {
    query: 'first query',
    queries: ['first query', 'second query'],
  }
  const result = {
    links: [
      { title: 'Found page', url: 'https://example.com/found' },
    ],
    summary: '',
  }
  const EXPANDED = {
    getMessageUiState: () => true,
  }

  function draw(row: ReturnType<typeof toolRow>) {
    return render(() => <ToolMessage row={row} context={EXPANDED} />).container
  }

  it('lists the further queries on the request row of a call that runs', () => {
    const row = toolRow(toolCallFixture('web_search', { status: 'in_progress', request }), 'request')
    expect(draw(row).textContent).toContain('second query')
    expect(toolCallMeta(row).collapsible).toBe(true)
  })

  it('keeps the further queries on the request row after the call ends', () => {
    const row = toolRow(toolCallFixture('web_search', { request, result }), 'request')
    expect(draw(row).textContent).toContain('second query')
  })

  it('draws only what the search found on a result row beside its request row', () => {
    const row = toolRow(toolCallFixture('web_search', { request, result }), 'result', { request: true })
    const text = draw(row).textContent
    expect(text).toContain('Found page')
    expect(text).not.toContain('second query')
    expect(toolCallMeta(row).collapsible).toBe(false)
  })

  it('draws the queries and the links on a result row with no request row', () => {
    const row = toolRow(toolCallFixture('web_search', { request, result }), 'result')
    const text = draw(row).textContent
    expect(text).toContain('second query')
    expect(text).toContain('Found page')
    expect(toolCallMeta(row).collapsible).toBe(true)
  })

  it('offers no Expand for a search that ran one query', () => {
    const row = toolRow(toolCallFixture('web_search', {
      status: 'in_progress',
      request: {
        query: 'only query',
        queries: ['only query'],
      },
    }), 'request')
    expect(toolCallMeta(row).collapsible).toBe(false)
  })
})
