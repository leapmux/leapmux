import { existsSync, writeFileSync } from 'node:fs'

import { join } from 'node:path'
import { expect } from '@playwright/test'
import { CLINE_SPAWN_WARNING } from '../../../src/components/chat/providers/cline/spawnWarning'
import { CLINE_DECLINE_REASON } from '../../../src/generated/contracts/cline-protocol'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { CLINE_E2E_SKIP_REASON, clineTest } from '../cline-fixtures'
import { bashToolCall, readToolCall, spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { chatText, expectSettingsChip, messageBubbles, sendMessage, visibleControlBanner, waitForAgentIdle } from '../helpers/ui'

/**
 * The test answers real native permission requests. Allow executes the tool. Deny must reach the next native model request as a refusal.
 *
 * The Worker starts one private Cline hub for this agent. Cline's DeepSeek provider sends requests to the isolated mock.
 */
clineTest.skip(!!CLINE_E2E_SKIP_REASON, CLINE_E2E_SKIP_REASON || '')

const PROVIDER = AgentProvider.CLINE

clineTest.describe('Cline control requests', () => {
  clineTest('runs a command after the reader allows it', async ({ askingClineWorkspace, page, modelScript }) => {
    void askingClineWorkspace
    await expectSettingsChip(page, 'Act')
    await modelScript.queue(
      { toolCalls: [bashToolCall(PROVIDER, 'allow-call', 'echo "cline-$((40 + 2))"')] },
      { text: 'The command ran.' },
    )
    await sendMessage(page, modelScript.prompt('Run the arithmetic command.'))
    // The call waits on the banner, so the second step waits too.
    await modelScript.waitForSteps(1)

    await expect(visibleControlBanner(page)).toContainText('echo "cline-$((40 + 2))"')
    await expect(visibleControlBanner(page)).toContainText('run_commands')
    await page.getByTestId('control-allow-btn').filter({ visible: true }).click()

    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)
    await expect(visibleControlBanner(page)).toHaveCount(0)
    await expect.poll(() => chatText(page)).toContain('cline-42')
  })

  clineTest('refuses a command with the reader\'s reason, which reaches the model', async ({ askingClineWorkspace, page, modelScript }) => {
    const marker = join(askingClineWorkspace.workingDir, 'refused.txt')
    await modelScript.queue(
      { toolCalls: [bashToolCall(PROVIDER, 'deny-call', `printf refused > ${marker}`)] },
      { text: 'The command was refused.' },
    )
    await sendMessage(page, modelScript.prompt('Run the refused command.'))
    await modelScript.waitForSteps(1)
    await expect(visibleControlBanner(page)).toContainText(`printf refused > ${marker}`)

    // Text in the composer turns Deny into Send feedback, which refuses with it.
    await page.getByTestId('composer-editor').locator('.ProseMirror').fill('Use the clean target instead.')
    const deny = page.getByTestId('control-deny-btn').filter({ visible: true })
    await expect(deny).toHaveText('Send feedback')
    await deny.click()

    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)
    await expect(visibleControlBanner(page)).toHaveCount(0)
    // Cline hands the reason to the model as the call's error. The command never ran.
    const followUp = status.requests.find(request => request.stepIndex === 1)
    expect(JSON.stringify(followUp?.body)).toContain('Use the clean target instead.')
    await expect(messageBubbles(page).filter({ hasText: 'Use the clean target instead.' }).first()).toBeVisible()
    expect(existsSync(marker)).toBe(false)
  })

  clineTest('reads a file without a banner, as Cline\'s own CLI does in Act', async ({ askingClineWorkspace, page, modelScript }) => {
    const notes = join(askingClineWorkspace.workingDir, 'notes.txt')
    writeFileSync(notes, 'cline-safe-read\n')
    await modelScript.queue(
      { toolCalls: [readToolCall(PROVIDER, 'safe-read', notes)] },
      { text: 'I read the notes.' },
    )
    await sendMessage(page, modelScript.prompt('Read the notes.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)

    await expect(visibleControlBanner(page)).toHaveCount(0)
    await expect.poll(() => chatText(page)).toContain('cline-safe-read')
  })

  clineTest('warns that an approved subagent asks nothing, and a refusal reaches the model', async ({ askingClineWorkspace, page, modelScript }) => {
    void askingClineWorkspace
    await modelScript.queue(
      {
        toolCalls: [spawnSubagentToolCall(PROVIDER, 'spawn-refused', {
          description: 'Refused helper',
          prompt: 'Never runs, because the reader refuses the spawn.',
        })],
      },
      { text: 'The subagent was refused.' },
    )
    await sendMessage(page, modelScript.prompt('Start a helper subagent.'))
    await modelScript.waitForSteps(1)
    await expect(visibleControlBanner(page)).toContainText('spawn_agent')
    await expect(visibleControlBanner(page)).toContainText(CLINE_SPAWN_WARNING)
    await page.getByTestId('control-deny-btn').filter({ visible: true }).click()

    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)
    await expect(visibleControlBanner(page)).toHaveCount(0)
    // A refusal with no words of the reader's gives the model LeapMux's own reason.
    const followUp = status.requests.find(request => request.stepIndex === 1)
    expect(JSON.stringify(followUp?.body)).toContain(CLINE_DECLINE_REASON.Tool)
  })
})
