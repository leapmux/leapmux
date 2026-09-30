import { expect, test } from './fixtures'
import { expectCompactionNotice } from './helpers/compaction'
import { sendMessage, waitForAgentIdle } from './helpers/ui'

const OLD_CONTEXT_MARKER = 'LEAPMUXOLDCONTEXTCLAUDERIVER'
const SUMMARY_MARKER = 'Earlier Claude work summarized.'

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
      // The last user turn includes the prior prompt before the native
      // directive. Match the directive inside that turn.
      when: { user: 'CRITICAL: Respond with TEXT ONLY\\.' },
      respond: { text: SUMMARY_MARKER },
    })
    await modelScript.queue(
      { text: 'The first Claude turn ended.' },
      { text: 'The second Claude turn ended.' },
      { text: 'The third Claude turn ended.' },
    )
    await sendMessage(page, modelScript.prompt(`Keep ${OLD_CONTEXT_MARKER} in the older context.`))
    await modelScript.waitForSteps(1)
    await waitForAgentIdle(page)
    await sendMessage(page, modelScript.prompt('Add a newer turn before compaction.'))
    const before = await modelScript.waitForSteps(2)
    expect(JSON.stringify(before.requests.find(request => request.stepIndex === 1)?.body)).toContain(OLD_CONTEXT_MARKER)
    await waitForAgentIdle(page)
    await sendMessage(page, modelScript.prompt('Add the current turn before compaction.'))
    await modelScript.waitForSteps(3)
    await waitForAgentIdle(page)

    await sendMessage(page, '/compact')
    await waitForAgentIdle(page)

    await expectCompactionNotice(page)
    const compacted = await modelScript.status()
    expect(compacted.ruleMatches['compaction-summarizer']).toBeGreaterThan(0)

    await modelScript.queue({ text: 'The compacted Claude session continued.' })
    await sendMessage(page, modelScript.prompt('Continue after manual compaction.'))
    const continued = await modelScript.waitForSteps(4)
    const nextRequest = JSON.stringify(continued.requests.find(request => request.stepIndex === 3)?.body)
    expect(nextRequest).toContain(SUMMARY_MARKER)
    expect(nextRequest).not.toContain(OLD_CONTEXT_MARKER)
    await waitForAgentIdle(page)
  })
})
