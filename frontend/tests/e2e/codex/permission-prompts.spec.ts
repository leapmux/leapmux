import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codexTest } from '../codex-fixtures'
import { bashToolCall, codexEscalatedCommandToolCall } from '../helpers/providerToolCalls'
import { expectSettingsOptionChosen, isMaybeVisible, messageContents, openSettingsMenu, sendMessage, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'

const CODEX = AgentProvider.CODEX

function shellQuote(value: string): string {
  return `'${value.replaceAll(/'/g, String.raw`'\''`)}'`
}

function writeCommand(path: string, content: string): string {
  return `printf %s ${shellQuote(content)} > ${shellQuote(path)}`
}

codexTest.describe('codex permission requests', () => {
  codexTest('runs a safe command without an approval request', async ({ authenticatedCodexWorkspace, page, modelScript }) => {
    void authenticatedCodexWorkspace
    await waitForSettingsHydrated(page, 'permissionMode')
    await expectSettingsOptionChosen(page, 'permissionMode-on-request')

    await modelScript.queue(
      { toolCalls: [bashToolCall(CODEX, 'safe-command', 'printf %s codex-safe-42')] },
      { text: 'The safe command finished.' },
    )
    await sendMessage(page, modelScript.prompt('Run the safe command once.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expect(page.locator('[data-tool-message]:visible').filter({ hasText: 'codex-safe-42' }).first()).toBeVisible()
    await expect(page.locator('[data-testid="control-banner"]:visible')).toHaveCount(0)
  })

  codexTest('runs an escalated command only after approval', async ({ authenticatedCodexWorkspace, page, modelScript }) => {
    const workingDir = authenticatedCodexWorkspace.workingDir
    expect(workingDir).toBeTruthy()
    const file = join(workingDir!, 'approved-command.txt')
    await waitForSettingsHydrated(page, 'permissionMode')
    await expectSettingsOptionChosen(page, 'permissionMode-on-request')

    await modelScript.queue(
      { toolCalls: [codexEscalatedCommandToolCall('approve-command', writeCommand(file, 'approved-42'))] },
      { text: 'Approval completed.' },
    )
    await sendMessage(page, modelScript.prompt('Request approval for the scripted command.'))
    await modelScript.waitForSteps(1)

    const banner = page.locator('[data-testid="control-banner"]:visible')
    await expect(banner).toBeVisible()
    await expect(banner).toContainText('Run the scripted approval test.')
    expect(existsSync(file)).toBe(false)
    await page.getByTestId('control-allow-btn').click()
    await modelScript.waitForSteps(2)
    await waitForAgentIdle(page)

    expect(readFileSync(file, 'utf8')).toBe('approved-42')
    await expect(banner).toHaveCount(0)
  })

  codexTest('leaves the file absent after a denied escalated command', async ({ authenticatedCodexWorkspace, page, modelScript }) => {
    const workingDir = authenticatedCodexWorkspace.workingDir
    expect(workingDir).toBeTruthy()
    const file = join(workingDir!, 'denied-command.txt')
    await waitForSettingsHydrated(page, 'permissionMode')
    await expectSettingsOptionChosen(page, 'permissionMode-on-request')

    await modelScript.queue(
      { toolCalls: [codexEscalatedCommandToolCall('deny-command', writeCommand(file, 'denied-42'))] },
    )
    await sendMessage(page, modelScript.prompt('Request approval for the scripted command.'))
    await modelScript.waitForSteps(1)

    const banner = page.locator('[data-testid="control-banner"]:visible')
    await expect(banner).toBeVisible()
    expect(existsSync(file)).toBe(false)
    await page.getByTestId('control-deny-btn').click()
    // Codex ends this code-mode cell on denial. It sends no second model request.
    await waitForAgentIdle(page)

    expect(existsSync(file)).toBe(false)
    await expect(banner).toHaveCount(0)
  })
})

/** The command the approval test scripts. It never runs: the prompt stops it. */
const APPROVAL_COMMAND = `rm -${'rf'} /tmp/codex-approval-test-dir-nonexistent`

codexTest.describe('codex approval UI', () => {
  codexTest('approval flow works with on-request policy', async ({ authenticatedCodexWorkspace, page, modelScript }) => {
    void authenticatedCodexWorkspace

    // Switch to on-request approval policy so approval prompts appear.
    await openSettingsMenu(page, 'permissionMode')
    const onRequestRadio = page.locator('[data-testid="permissionMode-on-request"]')
    await expect(onRequestRadio).toBeVisible()
    await onRequestRadio.click()
    await waitForSettingsIdle(page)

    // Close the menu by clicking elsewhere.
    await page.locator('[data-testid="composer-editor"] .ProseMirror').click()

    // Send a command that will trigger an approval request.
    // Use rm which should always require approval in on-request mode.
    // What the test does with the banner decides how many turns follow.
    await modelScript.fallback({ text: 'The command finished.' })
    await modelScript.queue({ toolCalls: [bashToolCall(AgentProvider.CODEX, 'approval-call', APPROVAL_COMMAND)] })
    await sendMessage(page, modelScript.prompt('Run this exact command.'))
    await modelScript.waitForSteps()

    // Wait for the control banner to appear.
    const banner = page.locator('[data-testid="control-banner"]')
    await expect(banner).toBeVisible()

    // The allow-choice pills expose Codex's own decisions without the former
    // Remember switch. The group appears only when the CLI offers `accept` plus
    // a second allow decision, and which second one it offers is the CLI's
    // choice -- so the pills are checked ONLY when the group renders. The
    // approval round-trip below is what this spec exists for, and it must fail
    // on its own terms rather than on a missing radio.
    const allowChoices = page.getByRole('radiogroup', { name: 'Allow as' })
    if (await isMaybeVisible(allowChoices)) {
      const once = allowChoices.getByRole('radio', { name: 'Once' })
      await expect(once).toBeChecked()
      const remembering = allowChoices.getByRole('radio').nth(1)
      await remembering.click()
      await expect(remembering).toBeChecked()
      // Return to the one-turn decision before approval. A browser test must not
      // persist a real Codex command or host rule in the developer's account state.
      await once.click()
      await expect(once).toBeChecked()
    }

    const allowBtn = page.locator('[data-testid="control-allow-btn"]')
    await expect(allowBtn).toBeVisible()
    await allowBtn.click()

    // Wait for the agent to finish and verify the command ran.
    await waitForAgentIdle(page, 120_000)
    const chatArea = messageContents(page)
    await expect.poll(async () => (await chatArea.allTextContents()).join(' '))
      .toContain('codex-approval-test-dir-nonexistent')
  })
})
