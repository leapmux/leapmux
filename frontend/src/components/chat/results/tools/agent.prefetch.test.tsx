import type { ToolResultRenderContext } from '~/components/chat/renderContext'
import { render } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it } from 'vitest'
import { ToolMessage } from '~/components/chat/results/ToolMessage'
import { _resetMarkdownCache, getCachedMarkdownHtml } from '~/lib/renderMarkdown'
import { settleWorkerReplies } from '~/test-support/fakeWorker'
import { toolCallFixture, toolRow } from '~/test-support/toolCallFixture'

const PROMPT = 'Run the **tests**.'

/** The request row of a launch whose result row exists, so the prompt starts collapsed. */
function renderCollapsedPrompt(context: ToolResultRenderContext) {
  const call = toolCallFixture('agent', {
    request: { description: 'Fix the build', prompt: PROMPT },
  })
  return render(() => <ToolMessage row={toolRow(call, 'request', { result: true })} context={context} />)
}

// The collapsed request renders no prompt, so only its prefetch puts the HTML of
// the prompt in the shared cache, where an expand finds it.
describe('agentRenderer', () => {
  beforeEach(() => {
    _resetMarkdownCache()
  })

  it('prefetches the prompt that its collapsed request hides', async () => {
    renderCollapsedPrompt({})
    await settleWorkerReplies()

    expect(getCachedMarkdownHtml(PROMPT)).toBe('<p>Run the <strong>tests</strong>.</p>')
  })

  it('prefetches nothing in the hidden premeasure copy of a row', async () => {
    renderCollapsedPrompt({ premeasureMode: true })
    await settleWorkerReplies()

    expect(getCachedMarkdownHtml(PROMPT)).toBeUndefined()
  })
})
