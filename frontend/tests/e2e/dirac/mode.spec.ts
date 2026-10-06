import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { expect } from '@playwright/test'
import { DIRAC_MODE } from '../../../src/generated/contracts/dirac-protocol'
import { isObject } from '../../../src/lib/jsonPick'
import { diracTest } from '../dirac-fixtures'
import { ruleRequest } from '../helpers/mockModelScript'
import { expectSettingsOptionsOffered } from '../helpers/nativeSettings'
import { diracRespondToolCall } from '../helpers/providerToolCalls'
import { chooseSettingsOption, expectSettingsChip, expectSettingsOptionChosen, sendMessage, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'

diracTest.describe('Dirac settings', () => {
  diracTest('the mode menu lists Plan and Act', async ({ authenticatedDiracWorkspace, page }) => {
    void authenticatedDiracWorkspace
    await waitForSettingsHydrated(page)
    await expectSettingsOptionsOffered(page, 'permissionMode', Object.values(DIRAC_MODE))
    await expectSettingsOptionChosen(page, `permissionMode-${DIRAC_MODE.Act}`)
  })
})

diracTest.describe('Dirac settings apply', () => {
  function nativeCurrentMode(request: MockModelRequestRecord): string {
    expect(request.protocol).toBe('openai-chat-completions')
    const body = isObject(request.body) ? request.body : null
    const messages = Array.isArray(body?.messages) ? body.messages : []
    const user = messages.filter(isObject).findLast(message => message.role === 'user')
    const content = Array.isArray(user?.content) ? user.content : []
    const details = content.filter(isObject).find(part => typeof part.text === 'string' && part.text.includes('<environment_details>'))
    return typeof details?.text === 'string' ? details.text : ''
  }

  diracTest('sends plan and act modes in successive native requests', async ({ authenticatedDiracWorkspace, page, modelScript }) => {
    void authenticatedDiracWorkspace
    await waitForSettingsHydrated(page)

    await chooseSettingsOption(page, 'permissionMode-plan')
    await chooseSettingsOption(page, 'reasoning_effort-high')
    await waitForSettingsIdle(page)
    await modelScript.rule({
      name: 'dirac-mode-change-resume',
      when: { user: '^<environment_details>\\n# Current Mode\\nACT MODE' },
      respond: { toolCalls: [diracRespondToolCall('dirac-act-answer', 'complete', 'The Act check ended.')] },
      once: true,
    })
    const planStep = await modelScript.queue({ toolCalls: [diracRespondToolCall('dirac-plan-answer', 'plan', 'The Plan check ended.')] })
    await sendMessage(page, modelScript.prompt('Reply once in the selected mode.'))
    const planned = await modelScript.requestAt(planStep)
    expect(planned.body).toMatchObject({ reasoning_effort: 'high' })
    const planDetails = nativeCurrentMode(planned)
    expect(planDetails).toContain('# Current Mode\nPLAN MODE')
    expect(planDetails).not.toContain('# Current Mode\nACT MODE')

    await expect.poll(async () => (await modelScript.status()).ruleMatches['dirac-mode-change-resume'] ?? 0).toBe(1)
    await waitForAgentIdle(page)
    await expectSettingsChip(page, 'Act')
    const status = await modelScript.status()
    const actDetails = nativeCurrentMode(ruleRequest(status, 'dirac-mode-change-resume'))
    expect(actDetails).toContain('# Current Mode\nACT MODE')
    expect(actDetails).not.toContain('# Current Mode\nPLAN MODE')
  })
})
