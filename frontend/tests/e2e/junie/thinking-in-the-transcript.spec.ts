import { junieAnswerToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, bandRows, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { expect, JUNIE_E2E_SKIP_REASON, junieTest } from '../junie-fixtures'

junieTest.describe('Junie basic chat', () => {
  junieTest.skip(!!JUNIE_E2E_SKIP_REASON, JUNIE_E2E_SKIP_REASON || '')

  junieTest('does not expose model reasoning as an ACP thought row', async ({ authenticatedResponsesJunieWorkspace, page, modelScript }) => {
    void authenticatedResponsesJunieWorkspace
    const reasoning = 'JUNIE_THOUGHT_MARKER I compare the two values.'
    await modelScript.rule(
      { name: 'junie-capability-filter', when: { system: 'capability filter agent' }, respond: { text: '' } },
      { name: 'junie-task-name', when: { system: 'task description summarizer' }, respond: { text: 'Arithmetic task' } },
    )
    await modelScript.queue({ reasoning, toolCalls: [junieAnswerToolCall('junie-thought-answer', 'The answer is 6912.')] })
    await sendMessage(page, modelScript.prompt('Add 1234 and 5678.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    const request = status.requests.find(record => record.stepIndex === 0)
    expect(request?.path).toBe('/v1/responses')
    await expect(assistantBubbles(page).filter({ hasText: 'The answer is 6912.' }).first()).toBeVisible()
    // The ACP bridge emits thought chunks from system events. It does not send
    // the reasoning item of this model response as a thought row.
    await expect(bandRows(page, 'thought')).toHaveCount(0)
    await expect(bandRows(page, 'text').filter({ hasText: reasoning })).toHaveCount(0)
  })
})
