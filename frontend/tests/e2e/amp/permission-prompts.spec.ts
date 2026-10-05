import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'

import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { AMP_E2E_SKIP_REASON, ampTest } from '../amp-fixtures'
import { bashToolCall, editToolCall } from '../helpers/providerToolCalls'
import { chatText, sendMessage, visibleControlBanner, waitForAgentIdle } from '../helpers/ui'

/**
 * The test answers real native permission requests. Allow executes the tool. Deny must reach the next native model request as a refusal.
 *
 * The Worker drives Amp's stream JSON protocol. The isolated mock implements Amp's remote service.
 *
 * Amp delegates tool permissions to the Worker's helper. Ask raises a banner. Allow All answers at once. Workspace settings must not bypass Ask. Guarded files still follow these rules.
 */
ampTest.skip(!!AMP_E2E_SKIP_REASON, AMP_E2E_SKIP_REASON || '')

ampTest.describe('Amp permissions', () => {
  ampTest('runs a command after the reader allows it', async ({ askingAmpWorkspace, page, modelScript }) => {
    void askingAmpWorkspace
    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.AMP, 'allow-call', 'echo "amp-$((40 + 2))"')] },
      { text: 'The command ran.' },
    )
    await sendMessage(page, modelScript.prompt('Run the arithmetic command.'))
    // The call waits on the banner, so the second step waits too.
    await modelScript.waitForSteps(1)

    await expect(visibleControlBanner(page)).toContainText('echo "amp-$((40 + 2))"')
    await expect(visibleControlBanner(page)).toContainText('shell_command')
    await page.getByTestId('control-allow-btn').filter({ visible: true }).click()

    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expect(visibleControlBanner(page)).toHaveCount(0)
    await expect.poll(() => chatText(page)).toContain('amp-42')
  })

  ampTest('refuses a command with the reader\'s reason, which reaches the model', async ({ askingAmpWorkspace, page, modelScript }) => {
    void askingAmpWorkspace
    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.AMP, 'deny-call', 'echo "amp-$((50 + 5))"')] },
      { text: 'The command was refused.' },
    )
    await sendMessage(page, modelScript.prompt('Run the other arithmetic command.'))
    await modelScript.waitForSteps(1)
    await expect(visibleControlBanner(page)).toContainText('echo "amp-$((50 + 5))"')

    // Text in the composer turns Deny into Send feedback, which refuses with it.
    await page.getByTestId('composer-editor').locator('.ProseMirror').fill('Use the clean target instead.')
    const deny = page.getByTestId('control-deny-btn').filter({ visible: true })
    await expect(deny).toHaveText('Send feedback')
    await deny.click()

    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expect(visibleControlBanner(page)).toHaveCount(0)
    // The helper refused with the reason, and Amp handed it to the model as the
    // call's result. The command never ran.
    const followUp = status.requests.find(request => request.stepIndex === 1)
    expect(JSON.stringify(followUp?.body)).toContain('Use the clean target instead.')
    await expect.poll(() => chatText(page)).toContain('Use the clean target instead.')
    expect(await chatText(page)).not.toContain('amp-55')
  })

  ampTest('refuses a turn while the workspace settings allow every call', async ({ askingAmpWorkspace, page, modelScript }) => {
    const settings = join(askingAmpWorkspace.workingDir, '.amp', 'settings.json')
    mkdirSync(join(askingAmpWorkspace.workingDir, '.amp'), { recursive: true })
    writeFileSync(settings, JSON.stringify({ 'amp.dangerouslyAllowAll': true }))

    await sendMessage(page, modelScript.prompt('Run the refused command.'))
    const failed = page.getByTestId(/^queued-input-/).filter({ hasText: 'Failed' })
    await expect(failed).toContainText('amp.dangerouslyAllowAll')
    await expect(failed).toContainText('Allow All')
    // The agent refused before Amp started, so no model call happened.
    expect((await modelScript.status()).requests).toHaveLength(0)
    await expect(visibleControlBanner(page)).toHaveCount(0)
  })

  ampTest('asks before an edit of a file that Amp guards', async ({ askingAmpWorkspace, page, modelScript }) => {
    const envFile = join(askingAmpWorkspace.workingDir, '.env')
    writeFileSync(envFile, 'TOKEN=old\n')
    await modelScript.queue(
      { toolCalls: [editToolCall(AgentProvider.AMP, 'guarded-edit', { path: envFile, before: 'TOKEN=old', after: 'TOKEN=new' })] },
      { text: 'The file changed.' },
    )
    await sendMessage(page, modelScript.prompt('Change the token in the env file.'))
    await modelScript.waitForSteps(1)

    // Amp's own guard would ask through a dialog that stream-JSON mode cannot
    // answer, and the session would end. The banner asks instead.
    await expect(visibleControlBanner(page)).toContainText('apply_patch')
    await page.getByTestId('control-allow-btn').filter({ visible: true }).click()
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expect.poll(() => readFileSync(envFile, 'utf8')).toBe('TOKEN=new\n')
  })
})
