import type { WebSearchLink } from '../../model/tools/webSearch'
import { render } from '@solidjs/testing-library'
import { beforeAll, describe, expect, it } from 'vitest'
import { settleWorkerReplies } from '~/test-support/fakeWorker'
import { checkKindModule } from '~/test-support/kindTestHarness'
import { toolCallFixture, toolRow } from '~/test-support/toolCallFixture'
import { COLLAPSED_RESULT_ROWS } from '../collapse'
import { ToolMessage } from '../ToolMessage'
import { toolCallMeta } from './meta'

const COLLAPSED = {
  getMessageUiState: () => false,
}
const EXPANDED = {
  getMessageUiState: () => true,
}

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

  it('lists a first keyword that differs from the objective in the header', () => {
    const row = toolRow(toolCallFixture('web_search', {
      status: 'in_progress',
      request: {
        query: 'Find project documentation',
        queries: ['leapmux'],
      },
    }), 'request')

    expect(toolCallMeta(row).collapsible).toBe(true)
    expect(draw(row).textContent).toContain('leapmux')
  })
})

describe('web_search result expandability', () => {
  const request = { query: 'LeapMux' }
  const oneLink = { title: 'LeapMux Docs', url: 'https://leapmux.dev/docs' }

  function resultRow(links: WebSearchLink[], summary: string) {
    return toolRow(toolCallFixture('web_search', {
      request,
      result: { links, summary },
    }), 'result', { request: true })
  }

  it('marks a result with one link and a short summary as collapsible', async () => {
    const row = resultRow([oneLink], 'Short summary.')
    expect(toolCallMeta(row).collapsible).toBe(true)

    const collapsed = render(() => <ToolMessage row={row} context={COLLAPSED} />).container
    expect(collapsed.textContent).toContain('LeapMux Docs')
    expect(collapsed.textContent).not.toContain('Short summary.')

    const expanded = render(() => <ToolMessage row={row} context={EXPANDED} />).container
    await settleWorkerReplies()
    expect(expanded.textContent).toContain('Short summary.')
  })

  it('marks a summary-only result with no links as collapsible', () => {
    const row = resultRow([], 'Short summary.')
    expect(toolCallMeta(row).collapsible).toBe(true)
  })

  it('keeps an empty result with no links and no summary non-collapsible', () => {
    const row = resultRow([], '')
    expect(toolCallMeta(row).collapsible).toBe(false)
  })

  it('treats a whitespace-only summary as non-collapsible and falls back to links for copy text', () => {
    const row = resultRow([oneLink], '  \n\t ')
    const meta = toolCallMeta(row)
    expect(meta.collapsible).toBe(false)
    expect(meta.copyableContent?.()).toBe('LeapMux Docs\nhttps://leapmux.dev/docs')
  })

  it('enforces the collapsed link threshold when summary is empty', () => {
    const makeLinks = (count: number) => Array.from({ length: count }, (_, index) => ({
      title: `Result ${index + 1}`,
      url: `https://leapmux.dev/r/${index + 1}`,
    }))
    const atLimit = resultRow(makeLinks(COLLAPSED_RESULT_ROWS), '')
    const aboveLimit = resultRow(makeLinks(COLLAPSED_RESULT_ROWS + 1), '')

    expect(toolCallMeta(atLimit).collapsible).toBe(false)
    expect(toolCallMeta(aboveLimit).collapsible).toBe(true)
  })
})
