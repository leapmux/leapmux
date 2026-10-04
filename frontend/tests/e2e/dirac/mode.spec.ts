import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { isObject } from '../../../src/lib/jsonPick'
import { DIRAC_E2E_SKIP_REASON, diracTest, expect, openDiracAgent } from '../dirac-fixtures'
import { diracRespondToolCall } from '../helpers/providerToolCalls'
import { chooseSettingsOption, expectSettingsChip, openSettingsMenu, openWorkspace, sendMessage, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'

diracTest.describe('Dirac settings', () => {
  diracTest.skip(!!DIRAC_E2E_SKIP_REASON, DIRAC_E2E_SKIP_REASON || '')

  diracTest('the mode menu lists Plan and Act', async ({ page, authenticatedEmptyWorkspace, leapmuxServer }) => {
    await openDiracAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await waitForSettingsHydrated(page)

    const group = await openSettingsMenu(page, 'permissionMode')
    await expect(group.locator('[data-testid="permissionMode-act"] input[type="radio"]')).toBeChecked()
    await expect(group.locator('[data-testid="permissionMode-plan"] input[type="radio"]')).toBeVisible()
  })
})

diracTest.describe('Dirac settings apply', () => {
  diracTest.skip(!!DIRAC_E2E_SKIP_REASON, DIRAC_E2E_SKIP_REASON || '')

  function nativeCurrentMode(request: MockModelRequestRecord | undefined): string {
    expect(request?.protocol).toBe('openai-chat-completions')
    const body = isObject(request?.body) ? request.body : null
    const messages = Array.isArray(body?.messages) ? body.messages : []
    const user = messages.filter(isObject).findLast(message => message.role === 'user')
    const content = Array.isArray(user?.content) ? user.content : []
    const details = content.filter(isObject).find(part => typeof part.text === 'string' && part.text.includes('<environment_details>'))
    return typeof details?.text === 'string' ? details.text : ''
  }

  diracTest('sends plan and act modes in successive native requests', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openDiracAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
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
    await modelScript.queue({ toolCalls: [diracRespondToolCall('dirac-plan-answer', 'plan', 'The Plan check ended.')] })
    await sendMessage(page, modelScript.prompt('Reply once in the selected mode.'))
    const planned = await modelScript.waitForSteps(1)
    expect(planned.requests.find(request => request.stepIndex === 0)?.body).toMatchObject({ reasoning_effort: 'high' })
    const planDetails = nativeCurrentMode(planned.requests.find(request => request.stepIndex === 0))
    expect(planDetails).toContain('# Current Mode\nPLAN MODE')
    expect(planDetails).not.toContain('# Current Mode\nACT MODE')

    await expect.poll(async () => (await modelScript.status()).ruleMatches['dirac-mode-change-resume'] ?? 0).toBe(1)
    await waitForAgentIdle(page)
    await expectSettingsChip(page, 'Act')
    const status = await modelScript.status()
    const actDetails = nativeCurrentMode(status.requests.find(request => request.rule === 'dirac-mode-change-resume'))
    expect(actDetails).toContain('# Current Mode\nACT MODE')
    expect(actDetails).not.toContain('# Current Mode\nPLAN MODE')
  })
})
