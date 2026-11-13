import type { ToolResultRenderContext } from '../renderContext'
import { render } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it } from 'vitest'
import { _resetMarkdownCache, getCachedMarkdownHtml } from '~/lib/renderMarkdown'
import { recordWorkerRequests, settleWorkerReplies } from '~/test-support/fakeWorker'
import { WebSearchResultsBody } from './webSearchResults'

const SUMMARY = 'The **summary** of the search.'

const COLLAPSED: ToolResultRenderContext = {
  getMessageUiState: () => false,
}

function renderSummary(context: ToolResultRenderContext) {
  return render(() => (
    <WebSearchResultsBody
      source={{ links: [], summary: SUMMARY }}
      context={context}
    />
  ))
}

// The collapsed body renders nothing, so only its prefetch puts the HTML of the
// summary in the shared cache, where an expand finds it.
describe('WebSearchResultsBody', () => {
  beforeEach(() => {
    _resetMarkdownCache()
  })

  it('prefetches the summary that its collapsed body hides', async () => {
    renderSummary(COLLAPSED)
    await settleWorkerReplies()

    expect(getCachedMarkdownHtml(SUMMARY)).toBe('<p>The <strong>summary</strong> of the search.</p>')
  })

  it('prefetches nothing in the hidden premeasure copy of a row', async () => {
    renderSummary({ ...COLLAPSED, premeasureMode: true })
    await settleWorkerReplies()

    expect(getCachedMarkdownHtml(SUMMARY)).toBeUndefined()
  })

  it.each([
    ['collapsed', COLLAPSED],
    [
      'expanded',
      {
        getMessageUiState: () => true,
      },
    ],
  ] as const)('asks the Markdown worker nothing for a search with no summary, %s', async (_label, context) => {
    const requests = recordWorkerRequests('lib/markdownWorker.ts')

    render(() => (
      <WebSearchResultsBody
        source={{ links: [], summary: '' }}
        context={context}
      />
    ))
    await settleWorkerReplies()

    expect(requests).toEqual([])
  })

  it('asks the Markdown worker for the summary that its collapsed body hides', async () => {
    const requests = recordWorkerRequests('lib/markdownWorker.ts')

    renderSummary(COLLAPSED)
    await settleWorkerReplies()

    expect(requests).not.toEqual([])
  })
})
