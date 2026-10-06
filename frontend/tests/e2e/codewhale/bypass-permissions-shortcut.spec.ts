import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codewhaleTest, expectCodewhalePosture } from '../codewhale-fixtures'
import { bashToolCall } from '../helpers/providerToolCalls'
import { answerControl, expectNoControlBanner, sendMessage, toolRows, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'

const CODEWHALE = AgentProvider.CODEWHALE

codewhaleTest.describe('Codewhale approvals', () => {
  codewhaleTest('applies the bypass pill when the reader allows', async ({ authenticatedCodewhaleWorkspace, page, modelScript }) => {
    void authenticatedCodewhaleWorkspace
    const asked = await modelScript.queue(
      { toolCalls: [bashToolCall(CODEWHALE, 'bypass-call', 'touch bypass.txt && echo "bypass-$((40 + 2))"')] },
      { text: 'I created bypass.txt.' },
    )
    await sendMessage(page, modelScript.prompt('Create bypass.txt.'))
    await modelScript.waitForSteps(asked + 1)

    await waitForControlBanner(page)
    const pills = page.getByRole('radiogroup', { name: 'Permissions' })
    await expect(pills.getByRole('radio', { name: 'Unchanged' })).toBeChecked()
    const bypass = pills.getByRole('radio', { name: 'Bypass' })
    await bypass.click()
    await expect(bypass).toBeChecked()
    await answerControl(page, 'allow')
    await expectNoControlBanner(page)
    await modelScript.waitForSteps(asked + 2)
    await waitForAgentIdle(page)
    await expect(toolRows(page).filter({ hasText: 'bypass-42' }).first()).toBeVisible()
    // The same posture that the composer menu's bypass shortcut lands on.
    await expectCodewhalePosture(page, 'full_access')

    // Full Access runs a command that writes without asking.
    const unasked = await modelScript.queue(
      { toolCalls: [bashToolCall(CODEWHALE, 'unasked-call', 'touch unasked.txt && echo "unasked-$((40 + 2))"')] },
      { text: 'I created unasked.txt.' },
    )
    await sendMessage(page, modelScript.prompt('Create unasked.txt.'))
    await modelScript.waitForSteps(unasked + 2)
    await waitForAgentIdle(page)
    await expect(toolRows(page).filter({ hasText: 'unasked-42' }).first()).toBeVisible()
    await expectNoControlBanner(page)
  })
})
