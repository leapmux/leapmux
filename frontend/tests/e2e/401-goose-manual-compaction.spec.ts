import { expect, GOOSE_E2E_SKIP_REASON, gooseTest } from './goose-fixtures'
import { assistantBubbles, sendMessage, waitForAgentIdle } from './helpers/ui'

const OLD_CONTEXT_MARKER = 'LEAPMUXOLDCONTEXTGOOSERIVER'
const SUMMARY_MARKER = 'Goose summary retained the topic.'
const SUMMARY_RESPONSE = [
  '<analysis>Keep the earlier topic.</analysis>',
  '```json',
  JSON.stringify({ user_intent: [SUMMARY_MARKER], current_work: 'Continue the test.' }),
  '```',
].join('\n')

gooseTest.skip(!!GOOSE_E2E_SKIP_REASON, GOOSE_E2E_SKIP_REASON || '')

gooseTest.describe('Goose manual compaction', () => {
  gooseTest('runs the native slash command and removes old context', async ({ authenticatedGooseWorkspace, page, modelScript }) => {
    void authenticatedGooseWorkspace
    await modelScript.rule({
      name: 'goose-manual-summary',
      when: { user: '^Please summarize the conversation history provided in the system prompt\\.$' },
      respond: { text: SUMMARY_RESPONSE },
    })
    await modelScript.queue(
      { text: 'The first Goose turn ended.' },
      { text: 'The second Goose turn ended.' },
      { text: 'The third Goose turn ended.' },
    )
    for (const [index, prompt] of [`Keep ${OLD_CONTEXT_MARKER} in the older context.`, 'Add another context turn.', 'Finish the earlier context.'].entries()) {
      await sendMessage(page, modelScript.prompt(prompt))
      const status = await modelScript.waitForSteps(index + 1)
      if (index === 1) {
        expect(JSON.stringify(status.requests.find(request => request.stepIndex === index)?.body)).toContain(OLD_CONTEXT_MARKER)
      }
      await waitForAgentIdle(page)
    }

    await sendMessage(page, '/compact')
    await expect(assistantBubbles(page).filter({ hasText: 'Compaction complete' }).first()).toBeVisible()
    const compacted = await modelScript.status()
    expect(compacted.ruleMatches['goose-manual-summary']).toBeGreaterThan(0)
    await waitForAgentIdle(page)

    await page.reload()
    await expect(assistantBubbles(page).filter({ hasText: 'Compaction complete' }).first()).toBeVisible()
    await modelScript.queue({ text: 'The compacted Goose session continued.' })
    await sendMessage(page, modelScript.prompt('Continue after manual compaction.'))
    const continued = await modelScript.waitForSteps(4)
    const nextRequest = JSON.stringify(continued.requests.find(request => request.stepIndex === 3)?.body)
    expect(nextRequest).toContain(SUMMARY_MARKER)
    expect(nextRequest).not.toContain(OLD_CONTEXT_MARKER)
    await waitForAgentIdle(page)
  })
})
