import { bandRows, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { expect, qoderTest } from '../qoder-fixtures'

qoderTest.describe('Qoder CLI basic chat', () => {
  qoderTest('draws model reasoning in a thought band', async ({ qoderWorkspace, page, modelScript }) => {
    void qoderWorkspace
    const reasoning = 'QODER_THOUGHT_MARKER I compare the two values.'
    await modelScript.queue({ reasoning, text: 'The answer is 6912.' })
    await sendMessage(page, modelScript.prompt('Add 1234 and 5678.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expect(bandRows(page, 'thought').filter({ hasText: reasoning }).first()).toBeVisible()
    await expect(bandRows(page, 'text').filter({ hasText: reasoning })).toHaveCount(0)
  })
})
