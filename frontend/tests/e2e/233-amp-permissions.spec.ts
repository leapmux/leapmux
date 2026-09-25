import type { Page } from '@playwright/test'
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { AMP_E2E_SKIP_REASON, ampTest, expect } from './amp-fixtures'
import { bashToolCall, editToolCall } from './helpers/providerToolCalls'
import { applyPermissionPreset, expectSettingsChip, messageContents, openPlusMenu, sendMessage, waitForAgentIdle } from './helpers/ui'

/**
 * 233 — Amp permissions.
 *
 * Amp asks for no permission in stream-JSON mode. Its `delegate` permission rule runs
 * the LeapMux helper -- the worker's own executable -- for each call that its executor
 * runs, and the helper asks the agent over its bridge. In Ask the agent raises a
 * banner and answers the helper with the reader's decision. In Allow All the agent
 * answers at once. A refusal's typed reason reaches the model as the call's result.
 *
 * A repository's `.amp/settings.json` merges over the worker's settings, so in Ask the
 * agent refuses a turn while the workspace holds a setting that allows a call without
 * the banner. A guarded file of Amp, such as `.env`, reaches the banner too, because
 * the worker's settings send every edit to the permission rules.
 */
ampTest.skip(!!AMP_E2E_SKIP_REASON, AMP_E2E_SKIP_REASON || '')

/** The visible chat text, joined. */
async function chatText(page: Page): Promise<string> {
  return (await messageContents(page).allTextContents()).join(' ')
}

/** The control banner on screen. The chat renders each unmeasured row twice. */
function banner(page: Page) {
  return page.getByTestId('control-banner').filter({ visible: true })
}

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

    await expect(banner(page)).toContainText('echo "amp-$((40 + 2))"')
    await expect(banner(page)).toContainText('shell_command')
    await page.getByTestId('control-allow-btn').filter({ visible: true }).click()

    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)
    await expect(banner(page)).toHaveCount(0)
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
    await expect(banner(page)).toContainText('echo "amp-$((50 + 5))"')

    // Text in the composer turns Deny into Send feedback, which refuses with it.
    await page.getByTestId('composer-editor').locator('.ProseMirror').fill('Use the clean target instead.')
    const deny = page.getByTestId('control-deny-btn').filter({ visible: true })
    await expect(deny).toHaveText('Send feedback')
    await deny.click()

    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)
    await expect(banner(page)).toHaveCount(0)
    // The helper refused with the reason, and Amp handed it to the model as the
    // call's result. The command never ran.
    const followUp = status.requests.find(request => request.stepIndex === 1)
    expect(JSON.stringify(followUp?.body)).toContain('Use the clean target instead.')
    await expect.poll(() => chatText(page)).toContain('Use the clean target instead.')
    expect(await chatText(page)).not.toContain('amp-55')
  })

  ampTest('runs every call without a banner in Allow All, which the Bypass shortcut selects', async ({ askingAmpWorkspace, page, modelScript }) => {
    void askingAmpWorkspace
    // Amp has no model axis, so the mode chip is the sign that the settings arrived.
    await expectSettingsChip(page, 'Medium')

    // Amp has no mode that asks for the risky calls alone, so it offers no Smart
    // shortcut. Bypass selects Allow All, which applies to the next call at once.
    const menu = await openPlusMenu(page)
    await expect(menu.getByTestId('composer-smart-permissions')).toHaveCount(0)
    await expect(menu.getByTestId('composer-bypass-permissions')).toBeVisible()
    await page.keyboard.press('Escape')
    await applyPermissionPreset(page, 'bypass')

    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.AMP, 'bypass-call', 'echo "amp-$((60 + 6))"')] },
      { text: 'The command ran without a banner.' },
    )
    await sendMessage(page, modelScript.prompt('Run the third arithmetic command.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)

    await expect(banner(page)).toHaveCount(0)
    await expect.poll(() => chatText(page)).toContain('amp-66')
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
    await expect(banner(page)).toHaveCount(0)
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
    await expect(banner(page)).toContainText('apply_patch')
    await page.getByTestId('control-allow-btn').filter({ visible: true }).click()
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)
    await expect.poll(() => readFileSync(envFile, 'utf8')).toBe('TOKEN=new\n')
  })
})
