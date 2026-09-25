import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, assistantBubbles, bandRows, expectAssistantAnswer, sendMessage, visibleOnly, waitForAgentIdle } from './helpers/ui'
import { expect, KIRO_E2E_SKIP_REASON, kiroTest } from './kiro-fixtures'

kiroTest.skip(!!KIRO_E2E_SKIP_REASON, KIRO_E2E_SKIP_REASON || '')

/** The thinking that the first turn scripts. */
const REASONING = 'The sum is small.'

/**
 * The words that Kiro states for a throttled model call. Kiro adds the id of the
 * request after them, which differs for each call.
 */
const KIRO_THROTTLE_TEXT = 'Too many requests, please wait before trying again.'

/**
 * 223 -- Kiro basic chat.
 *
 * Kiro's engine calls its own service, which the mock answers with an AWS event
 * stream. A turn with thinking and text reaches the transcript, the next turn of the
 * same session carries the first one, and a model call that Kiro could not make
 * states its reason.
 */
kiroTest.describe('Kiro basic chat', () => {
  kiroTest('sends a message and receives the response', async ({ authenticatedKiroWorkspace, page, modelScript }) => {
    void authenticatedKiroWorkspace
    await modelScript.queue({ reasoning: REASONING, text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await waitForAgentIdle(page, 120_000)
    await expectAssistantAnswer(page)
    // The thinking is a row of its own, and the answer row holds the answer alone.
    await expect(bandRows(page, 'thought').filter({ hasText: REASONING }).first()).toBeVisible()
    await expect(bandRows(page, 'text').filter({ hasText: ARITHMETIC_ANSWER_TEXT }).filter({ hasText: REASONING })).toHaveCount(0)
  })

  kiroTest('continues the conversation in the same session', async ({ authenticatedKiroWorkspace, page, modelScript }) => {
    void authenticatedKiroWorkspace
    await modelScript.queue({ text: 'First answer.' }, { text: 'Second answer.' })
    await sendMessage(page, modelScript.prompt('Say the first answer.'))
    await waitForAgentIdle(page, 120_000)
    await expect(assistantBubbles(page).filter({ hasText: 'First answer.' })).toBeVisible()

    await sendMessage(page, modelScript.prompt('Say the second answer.'))
    await waitForAgentIdle(page, 120_000)
    await expect(assistantBubbles(page).filter({ hasText: 'Second answer.' })).toBeVisible()
    // Kiro sends the history of the session with each turn.
    const second = JSON.stringify((await modelScript.status()).requests.at(-1)?.body)
    expect(second).toContain('First answer.')
  })

  // Kiro shows a throttle as a display error, and then fails the prompt with the
  // same words. The transcript states them, not a generic prompt failure.
  kiroTest('states the reason of a model call that the service throttled', async ({ authenticatedKiroWorkspace, page, modelScript }) => {
    void authenticatedKiroWorkspace
    // Kiro sends a throttled call again by itself, and the number of tries is
    // Kiro's own, so the fallback throttles each one.
    await modelScript.fallback({ error: { status: 429, code: 'ThrottlingException', message: 'Rate exceeded' } })
    await sendMessage(page, modelScript.prompt('Say hello.'))
    await waitForAgentIdle(page, 120_000)
    await expect(visibleOnly(page.getByText(KIRO_THROTTLE_TEXT, { exact: false })).first()).toBeVisible()
    expect((await modelScript.status()).requests.length, 'Kiro made the model call').toBeGreaterThan(0)
  })
})
