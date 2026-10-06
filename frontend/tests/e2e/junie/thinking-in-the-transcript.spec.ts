import { expect } from '@playwright/test'
import { junieAnswerToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, bandRows, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { junieTest } from '../junie-fixtures'

junieTest.describe('Junie basic chat', () => {
  junieTest('does not expose model reasoning as an ACP thought row', async ({ authenticatedResponsesJunieWorkspace, page, modelScript }) => {
    void authenticatedResponsesJunieWorkspace
    const reasoning = 'JUNIE_THOUGHT_MARKER I compare the two values.'
    const start = await modelScript.queue({ reasoning, toolCalls: [junieAnswerToolCall('junie-thought-answer', 'The answer is 6912.')] })
    await sendMessage(page, modelScript.prompt('Add 1234 and 5678.'))
    await modelScript.waitForSteps(start + 1)
    await waitForAgentIdle(page)

    expect((await modelScript.requestAt(start)).path).toBe('/v1/responses')
    await expect(assistantBubbles(page).filter({ hasText: 'The answer is 6912.' }).first()).toBeVisible()
    // The ACP bridge emits thought chunks from system events. It does not send
    // the reasoning item of this model response as a thought row.
    await expect(bandRows(page, 'thought')).toHaveCount(0)
    await expect(bandRows(page, 'text').filter({ hasText: reasoning })).toHaveCount(0)
  })
})
