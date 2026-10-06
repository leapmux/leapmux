import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codexTest } from '../codex-fixtures'
import { bashToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import { applyPermissionPreset, expectNoControlBanner, expectSettingsOptionChosen, sendMessage, waitForAgentIdle, waitForSettingsHydrated } from '../helpers/ui'

const CODEX = AgentProvider.CODEX

codexTest.describe('codex permission requests', () => {
  // A write inside the workspace can pass under a sandbox that permits workspace writes, so it cannot show the bypass
  // preset. The proof writes outside the workspace, which only the full-access sandbox permits.
  codexTest('writes outside the workspace after the bypass preset applies', async ({ authenticatedCodexWorkspace, page, modelScript }) => {
    void authenticatedCodexWorkspace
    const file = join(createTestDirectory('codex-bypass-output-'), 'result.txt')
    await waitForSettingsHydrated(page, 'permissionMode')
    await applyPermissionPreset(page, 'bypass')
    await expectSettingsOptionChosen(page, 'permissionMode-never')
    await expectSettingsOptionChosen(page, 'sandbox_policy-danger-full-access')
    await expectSettingsOptionChosen(page, 'network_access-enabled')

    const start = await modelScript.queue(
      { toolCalls: [bashToolCall(CODEX, 'bypass-command', `printf %s ${quotePosixShellArgument('bypass-42')} > ${quotePosixShellArgument(file)}`)] },
      { text: 'Bypass command finished.' },
    )
    await sendMessage(page, modelScript.prompt('Write the scripted marker outside this workspace.'))
    await modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(page)

    expect(readFileSync(file, 'utf8')).toBe('bypass-42')
    await expectNoControlBanner(page)
  })
})
