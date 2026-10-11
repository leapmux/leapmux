import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { expect } from '@playwright/test'
import { isObject } from '../../../src/lib/jsonPick'
import { claudeProcessTest as test } from '../claude-fixtures'
import { chooseSettingsOption, sendMessage, waitForAgentIdle, waitForSettingsIdle } from '../helpers/ui'

function nativeSystemInstructions(request: MockModelRequestRecord): string {
  expect(request.protocol).toBe('anthropic-messages')
  const body = isObject(request.body) ? request.body : null
  const messages = Array.isArray(body?.messages) ? body.messages : []
  return JSON.stringify(messages.filter(message => isObject(message) && message.role === 'system'))
}

test.describe('Agent Settings', () => {
  test('plan mode reaches the native request after a default turn', async ({ authenticatedWorkspace, page, modelScript }) => {
    void authenticatedWorkspace
    const normal = await modelScript.queue({ text: 'The Default turn ended.' })
    await sendMessage(page, modelScript.prompt('Reply once in the selected mode.'))
    await modelScript.waitForSteps(normal + 1)
    await waitForAgentIdle(page)

    await chooseSettingsOption(page, 'permissionMode-plan')
    await waitForSettingsIdle(page)
    const planned = await modelScript.queue({ text: 'The Plan turn ended.' })
    await sendMessage(page, modelScript.prompt('Reply once in the selected mode.'))
    await modelScript.waitForSteps(planned + 1)
    await waitForAgentIdle(page)
    const marker = 'Plan mode is active. The user indicated'
    expect(nativeSystemInstructions(await modelScript.requestAt(normal))).not.toContain(marker)
    expect(nativeSystemInstructions(await modelScript.requestAt(planned))).toContain(marker)
  })
})
