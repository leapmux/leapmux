import { existsSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { askUserQuestionToolCall, bashToolCall } from './helpers/providerToolCalls'
import { expectSettingsChip, sendMessage, waitForAgentIdle, waitForControlBanner, waitForSettingsHydrated } from './helpers/ui'
import { expect, KIMI_E2E_SKIP_REASON, kimiTest, occurrences, stepRequestBody } from './kimi-fixtures'

kimiTest.skip(!!KIMI_E2E_SKIP_REASON, KIMI_E2E_SKIP_REASON || '')

const KIMI = AgentProvider.KIMI_CODE

/** The request body of every answer the script gave after the first, joined. */
function laterRequests(status: { requests: { stepIndex?: number, body: unknown }[] }, from: number): string {
  return status.requests.filter(request => (request.stepIndex ?? -1) >= from).map(request => JSON.stringify(request.body)).join('\n')
}

kimiTest.describe('answers Kimi Code approvals', () => {
  // Always Ask is the default, and it asks before every command.
  //
  // Each command that must RUN prints a number that its own text does not
  // state, such as `kimi-allowed-output-42` from `kimi-allowed-output-$((40 + 2))`.
  // The row's header and the model's own tool call both show the command, so a
  // marker that the command text holds matches whether or not the command ran.
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
    expect(laterRequests(status, 1)).toContain('kimi-allowed-output-42')
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
    expect(laterRequests(status, 1)).toContain('was not run because the user rejected the approval request')
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
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expect(page.locator('[data-testid="control-banner"]')).toHaveCount(0)
    await expect(page.locator('[data-tool-message]:visible').filter({ hasText: 'kimi-session-scope-42' })).not.toHaveCount(0)
    // The request after the second run repeats the first run's result, so the
    // SECOND run printed the marker only when that body holds it more often than
    // the body after the first run. A refusal prints nothing.
    const marker = 'kimi-session-scope-42'
    expect(occurrences(stepRequestBody(status.requests, 3), marker), 'the second run printed the marker')
      .toBeGreaterThan(occurrences(stepRequestBody(status.requests, 1), marker))
  })
})

kimiTest.describe('answers Kimi Code questions', () => {
  kimiTest('a selected answer reaches the model as the tool result', async ({ authenticatedKimiWorkspace, page, modelScript }) => {
    void authenticatedKimiWorkspace
    await modelScript.queue(
      {
        toolCalls: [askUserQuestionToolCall(KIMI, 'color-question', [{
          question: 'Which color do you prefer?',
          header: 'Color',
          options: [
            { label: 'Red', description: 'A warm color.' },
            { label: 'Blue', description: 'A cool color.' },
          ],
        }])],
      },
      { text: 'Recorded the color.' },
    )
    await sendMessage(page, modelScript.prompt('Ask me which color I prefer.'))
    await modelScript.waitForSteps(1)

    const banner = page.getByTestId('control-banner').filter({ visible: true })
    await expect(banner).toContainText('Which color do you prefer?')
    await expect(banner.getByText('A cool color.', { exact: true })).toBeVisible()
    await banner.getByTestId('question-option-Blue').click()
    await page.getByTestId('control-submit-btn').click()
    await expect(banner).toHaveCount(0)

    // The model's own tool call repeats every option label in each later request,
    // so a bare label is no proof. Kimi Code answers with
    // `{"answers":{"<question>":"<label>"}}` as the tool result TEXT, and the
    // encoded request body escapes each quote of that text.
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    const later = laterRequests(status, 1)
    expect(later).toContain('\\"Which color do you prefer?\\":\\"Blue\\"')
    expect(later).not.toContain('\\"Which color do you prefer?\\":\\"Red\\"')
  })
})
