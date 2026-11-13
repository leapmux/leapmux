import { describe, expect, it } from 'vitest'
import { getCachedMarkdownHtml, prefetchMarkdownHtml, requestMarkdownHtml } from '~/lib/renderMarkdown'
import { settleWorkerReplies } from './fakeWorker'
import { fillMarkdownRenderGate } from './markdownRenderGate'

describe('fillMarkdownRenderGate', () => {
  it('holds each later render until the release, then answers the order in which the worker received them', async () => {
    const gate = fillMarkdownRenderGate()
    prefetchMarkdownHtml('A prefetched body.')
    requestMarkdownHtml('A body that a reader needs now.')
    expect(getCachedMarkdownHtml('A prefetched body.')).toBeUndefined()

    // The gate posts the body that a reader needs before the prefetch that came first.
    expect(await gate.release()).toEqual(['A body that a reader needs now.', 'A prefetched body.'])
    // The release delivers every reply, the later renders' too.
    expect(getCachedMarkdownHtml('A body that a reader needs now.')).toBe('<p>A body that a reader needs now.</p>')
    expect(getCachedMarkdownHtml('A prefetched body.')).toBe('<p>A prefetched body.</p>')
  })

  it('answers no render when the test requests none after the two that take the slots', async () => {
    const gate = fillMarkdownRenderGate()

    expect(await gate.release()).toEqual([])
  })

  it('empties the Markdown cache, so that a body rendered before renders again', async () => {
    requestMarkdownHtml('A body rendered before.')
    await settleWorkerReplies()
    expect(getCachedMarkdownHtml('A body rendered before.')).toBe('<p>A body rendered before.</p>')

    const gate = fillMarkdownRenderGate()
    expect(getCachedMarkdownHtml('A body rendered before.')).toBeUndefined()
    requestMarkdownHtml('A body rendered before.')

    expect(await gate.release()).toEqual(['A body rendered before.'])
  })
})
