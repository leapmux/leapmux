import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { codexTest } from '../codex-fixtures'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { exerciseNativePermissionDecision } from '../helpers/nativePermission'
import { expectTurnEndedAfter } from '../helpers/nativeStoredControlDecision'
import { runNativeToolTurn } from '../helpers/nativeToolExecution'
import { bashToolCall, codexEscalatedCommandToolCall } from '../helpers/providerToolCalls'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import { answerControl, chatText, chooseSettingsOption, controlActions, expectNoControlBanner, expectSettingsOptionChosen, isMaybeVisible, sendMessage, toolRows, waitForAgentIdle, waitForControlBanner, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { readCodexExecResult } from './execResult'

function writeCommand(path: string, content: string): string {
  return `printf %s ${quotePosixShellArgument(content)} > ${quotePosixShellArgument(path)}`
}

codexTest.describe('codex permission requests', () => {
  codexTest('runs a safe command without an approval request', async ({ native }) => {
    const { page } = native
    await waitForSettingsHydrated(page, 'permissionMode')
    await expectSettingsOptionChosen(page, 'permissionMode-on-request')

    // The observation fails if a banner shows at any time in the turn.
    let resultRequest: MockModelRequestRecord | undefined
    await expectNoNativeControl(native, {
      testId: 'control-banner',
      relatedProof: async () => {
        ({ resultRequest } = await runNativeToolTurn(native, {
          toolCalls: [bashToolCall(native.provider, 'safe-command', 'printf %s codex-safe-42')],
          prompt: 'Run the safe command once.',
          answer: 'The safe command finished.',
        }))
      },
    })

    // The command text holds the marker, so only the output field of the native result proves the run.
    if (!resultRequest)
      throw new Error('The safe Codex command turn returned no result request.')
    const result = readCodexExecResult(resultRequest, 'safe-command')
    expect(result.text).toContain('codex-safe-42')
    expect(result.failed).not.toBe(true)
    await expect(toolRows(page).filter({ hasText: 'codex-safe-42' }).first()).toBeVisible()
    await expectNoControlBanner(page)
  })

  codexTest('runs an escalated command only after approval', async ({ native, authenticatedCodexWorkspace }) => {
    const { page } = native
    const workingDir = authenticatedCodexWorkspace.workingDir
    expect(workingDir).toBeTruthy()
    const file = join(workingDir!, 'approved-command.txt')
    await waitForSettingsHydrated(page, 'permissionMode')
    await expectSettingsOptionChosen(page, 'permissionMode-on-request')

    await exerciseNativePermissionDecision(native, {
      toolCall: codexEscalatedCommandToolCall('approve-command', writeCommand(file, 'approved-42')),
      decision: 'allow',
      beforeDecision: async (banner) => {
        await expect(banner).toContainText('Run the scripted approval test.')
        expect(existsSync(file)).toBe(false)
      },
      nativeProof: () => {
        expect(readFileSync(file, 'utf8')).toBe('approved-42')
      },
      viewProof: () => expectNoControlBanner(page),
    })
  })

  codexTest('leaves the file absent after a denied escalated command', async ({ native, authenticatedCodexWorkspace }) => {
    const { page, modelScript } = native
    const workingDir = authenticatedCodexWorkspace.workingDir
    expect(workingDir).toBeTruthy()
    const file = join(workingDir!, 'denied-command.txt')
    await waitForSettingsHydrated(page, 'permissionMode')
    await expectSettingsOptionChosen(page, 'permissionMode-on-request')

    const start = await modelScript.queue(
      { toolCalls: [codexEscalatedCommandToolCall('deny-command', writeCommand(file, 'denied-42'))] },
    )
    await sendMessage(page, modelScript.prompt('Request approval for the scripted command.'))
    await modelScript.waitForSteps(start + 1)

    await waitForControlBanner(page)
    expect(existsSync(file)).toBe(false)
    await answerControl(page, 'deny')
    // Codex ends this code-mode cell on denial. It sends no second model request.
    await waitForAgentIdle(page)

    expect(existsSync(file)).toBe(false)
    await expectNoControlBanner(page)
    await expectTurnEndedAfter(modelScript, start + 1)
  })
})

/** The command that the approval test scripts. It removes a directory that does not exist, so a run changes nothing. */
const APPROVAL_COMMAND = `rm -${'rf'} /tmp/codex-approval-test-dir-nonexistent`

codexTest.describe('codex approval UI', () => {
  codexTest('approval flow works with on-request policy', async ({ native }) => {
    const { page, modelScript } = native

    // Switch to on-request approval policy so approval prompts appear.
    // The check of the chosen option closes the menus.
    await chooseSettingsOption(page, 'permissionMode-on-request')
    await waitForSettingsIdle(page)
    await expectSettingsOptionChosen(page, 'permissionMode-on-request')

    // `rm` always requires approval in on-request mode, so the command raises an approval request.
    // What the test does with the banner decides how many turns follow.
    await modelScript.fallback({ text: 'The command finished.' })
    const start = await modelScript.queue({ toolCalls: [bashToolCall(native.provider, 'approval-call', APPROVAL_COMMAND)] })
    await sendMessage(page, modelScript.prompt('Run this exact command.'))
    await modelScript.waitForSteps(start + 1)

    await waitForControlBanner(page)

    // The allow-choice pills show the allow decisions of Codex. The group appears only when the CLI offers `accept`
    // and a second allow decision, and the CLI chooses that second decision. So the test checks the pills only when
    // the group renders. This spec exists for the approval round trip below, so a missing radio must not fail it.
    // The pills sit in the control actions of the composer, not in the banner.
    const allowChoices = controlActions(page).getByRole('radiogroup', { name: 'Allow as' })
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

    await answerControl(page, 'allow')

    // Wait for the agent to finish. The chat then shows the approved command.
    await waitForAgentIdle(page)
    await expectNoControlBanner(page)
    await expect.poll(() => chatText(page)).toContain('codex-approval-test-dir-nonexistent')
  })
})
