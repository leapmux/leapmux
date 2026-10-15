import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codexTest } from '../codex-fixtures'
import { expectTurnEndedAfter } from '../helpers/modelScriptFixture'
import { bashToolCall, codexEscalatedCommandToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import { applyPermissionPreset, expectNoControlBanner, expectSettingsOptionChosen, sendMessage, visibleOnly, waitForAgentIdle, waitForControlBanner, waitForSettingsHydrated } from '../helpers/ui'

const CODEX = AgentProvider.CODEX

function writeCommand(path: string, content: string): string {
  return `printf %s ${quotePosixShellArgument(content)} > ${quotePosixShellArgument(path)}`
}

codexTest.describe('codex permission requests', () => {
  // A write inside the workspace can pass under a profile that permits workspace writes, so it cannot show the bypass
  // preset. The proof writes outside the workspace, which only the full-access profile permits.
  codexTest('writes outside the workspace after the bypass preset applies', async ({ authenticatedCodexWorkspace, page, modelScript }) => {
    void authenticatedCodexWorkspace
    const file = join(createTestDirectory('codex-bypass-output-'), 'result.txt')
    await waitForSettingsHydrated(page, 'permissionMode')
    await applyPermissionPreset(page, 'bypass')
    await expectSettingsOptionChosen(page, 'permissionMode-never')
    await expectSettingsOptionChosen(page, 'permission_profile-:danger-full-access')

    const start = await modelScript.queue(
      { toolCalls: [bashToolCall(CODEX, 'bypass-command', writeCommand(file, 'bypass-42'))] },
      { text: 'Bypass command finished.' },
    )
    await sendMessage(page, modelScript.prompt('Write the scripted marker outside this workspace.'))
    await modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(page)

    expect(readFileSync(file, 'utf8')).toBe('bypass-42')
    await expectNoControlBanner(page)
  })

  // A running turn keeps the approval policy that it started with, so Codex still asks after the switch. Bypass answers
  // the request that waits and each later request of that turn, as Codex's own full access would.
  codexTest('approves the pending and later requests of a turn that started before bypass', async ({ authenticatedCodexWorkspace, page, modelScript }) => {
    void authenticatedCodexWorkspace
    const directory = createTestDirectory('codex-bypass-mid-turn-')
    const first = join(directory, 'first.txt')
    const second = join(directory, 'second.txt')
    await waitForSettingsHydrated(page, 'permissionMode')
    await expectSettingsOptionChosen(page, 'permissionMode-on-request')

    const start = await modelScript.queue(
      { toolCalls: [codexEscalatedCommandToolCall('bypass-pending', writeCommand(first, 'pending-42'))] },
      { toolCalls: [codexEscalatedCommandToolCall('bypass-later', writeCommand(second, 'later-42'))] },
      { text: 'Both commands finished.' },
    )
    await sendMessage(page, modelScript.prompt('Run both scripted commands outside this workspace.'))
    await modelScript.waitForSteps(start + 1)
    const banner = await waitForControlBanner(page)
    await expect(banner).toContainText('Run the scripted approval test.')

    await applyPermissionPreset(page, 'bypass')
    await expect(visibleOnly(page.getByText('Bypass permissions approved 1 pending request.'))).toBeVisible()
    await modelScript.waitForSteps(start + 3)
    await waitForAgentIdle(page)

    expect(readFileSync(first, 'utf8')).toBe('pending-42')
    expect(readFileSync(second, 'utf8')).toBe('later-42')
    await expectNoControlBanner(page)
    await expectTurnEndedAfter(modelScript, start + 3)
  })
})
