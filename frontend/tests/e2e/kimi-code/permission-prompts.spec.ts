import { existsSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { nativeModelBodiesAfter } from '../helpers/nativeScenario'
import { bashToolCall } from '../helpers/providerToolCalls'
import { expectSettingsChip, sendMessage, waitForAgentIdle, waitForControlBanner, waitForSettingsHydrated } from '../helpers/ui'
import { kimiTest, occurrences } from '../kimi-fixtures'

const KIMI = AgentProvider.KIMI_CODE

kimiTest.describe('answers Kimi Code approvals', () => {
  // Always Ask is the default. It asks before each command.
  // A command that must run calculates its output marker. For example, `kimi-allowed-output-$((40 + 2))` produces `kimi-allowed-output-42`.
  // The row header and the model call contain the command text. A marker already in that text cannot prove execution.
  kimiTest.beforeEach(async ({ authenticatedKimiWorkspace, page }) => {
    void authenticatedKimiWorkspace
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Always Ask')
  })

  kimiTest('an allowed command runs, and its output reaches the model', async ({ page, modelScript }) => {
    await modelScript.queue(
      { toolCalls: [bashToolCall(KIMI, 'allow-call', 'echo "kimi-allowed-output-$((40 + 2))"')] },
      { text: 'The allowed command ran.' },
    )
    await sendMessage(page, modelScript.prompt('Run the echo command.'))
    await modelScript.waitForSteps(1)

    const banner = await waitForControlBanner(page)
    await expect(banner).toContainText('Bash')
    await expect(banner).toContainText('echo "kimi-allowed-output-$((40 + 2))"')
    await page.getByTestId('control-allow-btn').click()
    await expect(page.locator('[data-testid="control-banner"]')).not.toBeVisible()

    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expect(page.locator('[data-tool-message]:visible').filter({ hasText: 'kimi-allowed-output-42' }).first()).toBeVisible()
    expect(nativeModelBodiesAfter(status, 1)).toContain('kimi-allowed-output-42')
  })

  kimiTest('a denied command does not run, and the model learns of the denial', async ({ authenticatedKimiWorkspace, page, modelScript }) => {
    const marker = join(authenticatedKimiWorkspace.workingDir, 'kimi-denied-marker')
    await modelScript.queue(
      { toolCalls: [bashToolCall(KIMI, 'deny-call', 'touch kimi-denied-marker')] },
      { text: 'I stopped at the denial.' },
    )
    await sendMessage(page, modelScript.prompt('Create the marker file.'))
    await modelScript.waitForSteps(1)

    const banner = await waitForControlBanner(page)
    await expect(banner).toContainText('touch kimi-denied-marker')
    await page.getByTestId('control-deny-btn').click()
    await expect(page.locator('[data-testid="control-banner"]')).not.toBeVisible()

    // Kimi Code returns the denial as the tool result and asks the model again.
    // The result is the text of toolApprovalService.formatApprovalRejectionMessage.
    // The call id is no proof: the model's own tool call repeats it in every
    // later request.
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    expect(nativeModelBodiesAfter(status, 1)).toContain('was not run because the user rejected the approval request')
    expect(existsSync(marker), 'the denied command never ran in the working directory').toBe(false)
  })

  // The session scope adds a rule to the kap-server session, so the same
  // command in a later turn runs with no banner at all.
  //
  // The command has no quotes, unlike the commands above. A quoted command
  // that holds a parenthesis is impossible here, because Kimi Code never
  // matches the rule that it keeps for it. Kimi keeps the rule as
  // `Bash(<command>)`, with a backslash before each parenthesis, and matches
  // it with picomatch. Picomatch reads a double quote in a rule as a quoting
  // mark, and after an escaped parenthesis it drops the closing quote. So the
  // rule of `echo "x-$((40 + 2))"` never matches that command, and Kimi asks
  // again in the next turn, where no reader answers.
  kimiTest('an approval for the session covers the same command in the next turn', async ({ page, modelScript }) => {
    const command = 'echo kimi-session-scope-$((40 + 2))'
    await modelScript.queue(
      { toolCalls: [bashToolCall(KIMI, 'scope-first', command)] },
      { text: 'The first run finished.' },
    )
    await sendMessage(page, modelScript.prompt('Run the command once.'))
    await modelScript.waitForSteps(1)

    await waitForControlBanner(page)
    const scope = page.getByRole('radiogroup', { name: 'Allow scope' })
    await scope.getByRole('radio', { name: 'Session' }).click()
    await expect(scope.getByRole('radio', { name: 'Session' })).toBeChecked()
    await page.getByTestId('control-allow-btn').click()
    await expect(page.locator('[data-testid="control-banner"]')).not.toBeVisible()
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await modelScript.queue(
      { toolCalls: [bashToolCall(KIMI, 'scope-second', command)] },
      { text: 'The second run finished.' },
    )
    await sendMessage(page, modelScript.prompt('Run the same command again.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expect(page.locator('[data-testid="control-banner"]')).toHaveCount(0)
    await expect(page.locator('[data-tool-message]:visible').filter({ hasText: 'kimi-session-scope-42' })).not.toHaveCount(0)
    // The second request also contains the first run's result.
    // The second run must add another occurrence of the marker. A refusal prints no marker.
    const marker = 'kimi-session-scope-42'
    const body = async (step: number) => JSON.stringify((await modelScript.requestAt(step)).body)
    expect(occurrences(await body(3), marker), 'the second run printed the marker')
      .toBeGreaterThan(occurrences(await body(1), marker))
  })
})
