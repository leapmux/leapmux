import { expect } from '@playwright/test'

import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { CODEWHALE_E2E_SKIP_REASON, codewhaleTest, codewhaleToolMessages } from '../codewhale-fixtures'
import { nativeToolResultAt } from '../helpers/nativeToolExecution'
import { bashToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, sendMessage, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'

codewhaleTest.skip(!!CODEWHALE_E2E_SKIP_REASON, CODEWHALE_E2E_SKIP_REASON || '')

const CODEWHALE = AgentProvider.CODEWHALE

codewhaleTest.describe('Codewhale approvals', () => {
  // The Ask posture asks before a command that writes. A read-only command runs
  // without a banner, which is why each command below creates a file.
  //
  // Each command that must RUN prints a number that its own text does not state,
  // such as `approved-42` from `approved-$((40 + 2))`. The row's header shows the
  // command, so a marker that the command text holds matches the row whether or
  // not the command ran.
  codewhaleTest('runs a command that the reader allows', async ({ authenticatedCodewhaleWorkspace, page, modelScript }) => {
    void authenticatedCodewhaleWorkspace
    await modelScript.queue(
      { toolCalls: [bashToolCall(CODEWHALE, 'allow-call', 'touch approved.txt && echo "approved-$((40 + 2))"')] },
      { text: 'I created approved.txt.' },
    )
    await sendMessage(page, modelScript.prompt('Create approved.txt.'))
    await modelScript.waitForSteps(1)

    // The approval states no arguments of its own. The banner draws the command
    // from the call that the runtime reported before it asked.
    const banner = await waitForControlBanner(page)
    await expect(banner).toContainText('touch approved.txt')
    await page.getByTestId('control-allow-btn').click()
    await expect(page.locator('[data-testid="control-banner"]')).not.toBeVisible()

    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expect(codewhaleToolMessages(page).filter({ hasText: 'approved-42' }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'I created approved.txt.' })).toBeVisible()
  })

  codewhaleTest('refuses a command that the reader denies', async ({ authenticatedCodewhaleWorkspace, page, modelScript }) => {
    void authenticatedCodewhaleWorkspace
    await modelScript.queue(
      { toolCalls: [bashToolCall(CODEWHALE, 'deny-call', 'touch denied.txt')] },
      { text: 'The command was not approved.' },
    )
    await sendMessage(page, modelScript.prompt('Create denied.txt.'))
    await modelScript.waitForSteps(1)

    const banner = await waitForControlBanner(page)
    await expect(banner).toContainText('touch denied.txt')
    await page.getByTestId('control-deny-btn').click()
    await expect(page.locator('[data-testid="control-banner"]')).not.toBeVisible()

    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    // The runtime fails the call, and the model reads why.
    expect(await nativeToolResultAt(modelScript, 1, 'deny-call')).toContain('denied by user')
    await expect(assistantBubbles(page).filter({ hasText: 'The command was not approved.' })).toBeVisible()
  })
})
