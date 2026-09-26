import { test } from './fixtures'
import { expectCompactionNotice } from './helpers/compaction'
import { sendMessage, waitForAgentIdle } from './helpers/ui'

test.describe('Claude Code compaction notice', () => {
  // `/compact` is Claude's own slash command. The compaction summarizer is a
  // housekeeping turn the mock answers from a rule, so the content turn the
  // test scripts stays unconsumed. The notice the transcript draws comes from
  // the CLI's `compact_boundary` system message, not from the scripted reply.
  test('a manual compaction draws the context-compacted notice', async ({ authenticatedWorkspace, page, modelScript }) => {
    void authenticatedWorkspace
    // The summarizer can run more than once and is not the turn under test.
    await modelScript.rule({
      name: 'compaction-summarizer',
      when: { user: ['compact', 'summary', 'summarize'] },
      respond: { text: 'Earlier work summarized.' },
    })
    await modelScript.queue({ text: 'Ready to compact.' })
    await sendMessage(page, modelScript.prompt('Reply once, then I will compact.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await sendMessage(page, '/compact')
    await waitForAgentIdle(page)

    await expectCompactionNotice(page)
  })
})
