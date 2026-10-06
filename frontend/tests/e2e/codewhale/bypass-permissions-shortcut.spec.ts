import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codewhaleTest, codewhaleToolMessages, expectCodewhalePosture } from '../codewhale-fixtures'
import { bashToolCall } from '../helpers/providerToolCalls'
import { sendMessage, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'

const CODEWHALE = AgentProvider.CODEWHALE

codewhaleTest.describe('Codewhale approvals', () => {
  codewhaleTest('applies the bypass pill when the reader allows', async ({ authenticatedCodewhaleWorkspace, page, modelScript }) => {
    void authenticatedCodewhaleWorkspace
    await modelScript.queue(
      { toolCalls: [bashToolCall(CODEWHALE, 'bypass-call', 'touch bypass.txt && echo "bypass-$((40 + 2))"')] },
      { text: 'I created bypass.txt.' },
    )
    await sendMessage(page, modelScript.prompt('Create bypass.txt.'))
    await modelScript.waitForSteps(1)

    await waitForControlBanner(page)
    const pills = page.getByRole('radiogroup', { name: 'Permissions' })
    await expect(pills.getByRole('radio', { name: 'Unchanged' })).toBeChecked()
    const bypass = pills.getByRole('radio', { name: 'Bypass' })
    await bypass.click()
    await expect(bypass).toBeChecked()
    await page.getByTestId('control-allow-btn').click()
    await expect(page.locator('[data-testid="control-banner"]')).not.toBeVisible()
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expect(codewhaleToolMessages(page).filter({ hasText: 'bypass-42' }).first()).toBeVisible()
    // The same posture that the composer menu's bypass shortcut lands on.
    await expectCodewhalePosture(page, 'full_access')

    // Full Access runs a command that writes without asking.
    await modelScript.queue(
      { toolCalls: [bashToolCall(CODEWHALE, 'unasked-call', 'touch unasked.txt && echo "unasked-$((40 + 2))"')] },
      { text: 'I created unasked.txt.' },
    )
    await sendMessage(page, modelScript.prompt('Create unasked.txt.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expect(codewhaleToolMessages(page).filter({ hasText: 'unasked-42' }).first()).toBeVisible()
    await expect(page.locator('[data-testid="control-banner"]')).toHaveCount(0)
  })
})
