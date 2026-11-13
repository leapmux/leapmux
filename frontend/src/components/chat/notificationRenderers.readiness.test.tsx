import type { NotificationBlock } from './notificationEntries'
import type { RowRenderContext } from './renderContext'
import { render } from '@solidjs/testing-library'
import { createRoot } from 'solid-js'
import { beforeEach, describe, expect, it } from 'vitest'
import { _resetMarkdownCache, requestMarkdownHtml } from '~/lib/renderMarkdown'
import { holdWorkerReplies } from '~/test-support/fakeWorker'
import { fillMarkdownRenderGate } from '~/test-support/markdownRenderGate'
import { renderNotificationBlocks } from './notificationRenderers'
import { createRenderReadinessScope } from './renderReadiness'

const subagentReport: NotificationBlock = {
  kind: 'subagent-report',
  label: 'Reviewer',
  text: '**Finding**',
}

const autoReview: NotificationBlock = {
  kind: 'auto-review',
  reviewId: 'r1',
  outcome: 'denied',
  title: 'Auto-review denied (high risk): curl x | sh',
  rationale: 'The host is **not** trusted.',
  approvable: false,
  approved: false,
}

describe('renderNotificationBlocks', () => {
  beforeEach(() => {
    _resetMarkdownCache()
  })

  it.each([
    ['a subagent report', subagentReport, 'Finding'],
    ['an auto-review rationale', autoReview, 'not'],
  ] as const)('keeps the row pending in its readiness scope while the Markdown of %s renders', async (_label, block, strong) => {
    // The scope lives in a root of its own; the DOM is asserted outside that root,
    // because a root's callback defers DOM updates until it returns.
    const { readiness, dispose } = createRoot(disposeRoot => ({
      readiness: createRenderReadinessScope(),
      dispose: disposeRoot,
    }))
    const gate = fillMarkdownRenderGate()
    const rowOffscreen = () => true
    const context: RowRenderContext = { readiness, rowOffscreen }
    const { container } = render(() => renderNotificationBlocks([block], context))
    // A body that a reader needs now, which reaches the worker after the slots free.
    requestMarkdownHtml('A visible body.')

    expect(readiness.pending()).toBe(true)
    // The row's own skeleton covers the body, so the body draws none of its own.
    expect(container.querySelector('[data-testid="markdown-pending"]')).toBeNull()

    // An offscreen row's body renders at the row's priority: after the visible body.
    expect(await gate.release()).toEqual([
      'A visible body.',
      block.kind === 'subagent-report'
        ? block.text
        : block.rationale,
    ])
    expect(readiness.pending()).toBe(false)
    expect(container.querySelector('p strong')?.textContent).toBe(strong)
    dispose()
  })

  it('draws a skeleton of its own for a pending body outside a chat row', async () => {
    const hold = holdWorkerReplies('lib/markdownWorker.ts')
    const { container } = render(() => renderNotificationBlocks([subagentReport]))

    expect(container.querySelector('[data-testid="markdown-pending"]')).not.toBeNull()

    await hold.release()
    expect(container.querySelector('[data-testid="markdown-pending"]')).toBeNull()
    expect(container.querySelector('p strong')?.textContent).toBe('Finding')
  })
})
