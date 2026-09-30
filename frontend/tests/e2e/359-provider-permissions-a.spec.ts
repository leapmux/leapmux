import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from './agentSettings'
import { expect } from './fixtures'
import { openAgentViaAPI } from './helpers/api'
import { bashToolCall } from './helpers/providerToolCalls'
import { createTestDirectory } from './helpers/runDirectory'
import { assistantBubbles, chooseSettingsOption, openWorkspace, sendMessage, waitForAgentIdle, waitForSettingsIdle } from './helpers/ui'
import { createGitRepo } from './helpers/worktree'
import { OPENCODE_E2E_SKIP_REASON, opencodeTest } from './opencode-fixtures'
import { REASONIX_E2E_SKIP_REASON, reasonixTest } from './reasonix-fixtures'

function requireWorkingDir(path: string | undefined): string {
  if (!path)
    throw new Error('the provider workspace must have a working directory')
  return path
}

opencodeTest.describe('OpenCode permission prompt', () => {
  opencodeTest.skip(!!OPENCODE_E2E_SKIP_REASON, OPENCODE_E2E_SKIP_REASON || '')
  opencodeTest('asks before a shell command touches a file outside the working directory', async ({ authenticatedEmptyWorkspace, leapmuxServer, page, modelScript }) => {
    const parent = createTestDirectory('opencode-permission-')
    const workingDir = createGitRepo(parent, 'repo')
    const file = join(parent, 'opencode-permission-probe.txt')
    writeFileSync(file, 'remove this test file')
    await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, workingDir, {
      agentProvider: AgentProvider.OPENCODE,
      ...agentOpenOptions(agentSettings(AgentProvider.OPENCODE)),
    })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const command = 'rm ../opencode-permission-probe.txt'
    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.OPENCODE, 'opencode-permission', command)] },
      { text: 'The approved file removal finished.' },
    )
    await sendMessage(page, modelScript.prompt('Run the exact shell command in the scripted tool call.'))
    await modelScript.waitForSteps(1)

    const banner = page.getByTestId('control-banner').filter({ visible: true })
    await expect(banner).toContainText(command)
    await page.getByTestId('control-allow-btn').filter({ visible: true }).click()
    await modelScript.waitForSteps(2)
    await waitForAgentIdle(page)
    expect(existsSync(file)).toBe(false)
    await expect(assistantBubbles(page).filter({ hasText: 'The approved file removal finished.' }).first()).toBeVisible()
  })
})

reasonixTest.describe('Reasonix permission prompt', () => {
  reasonixTest.skip(!!REASONIX_E2E_SKIP_REASON, REASONIX_E2E_SKIP_REASON || '')
  reasonixTest('asks before a shell command writes a file in ask mode', async ({ authenticatedReasonixWorkspace, page, modelScript }) => {
    await chooseSettingsOption(page, 'tool_approval-ask')
    await waitForSettingsIdle(page)
    const file = join(requireWorkingDir(authenticatedReasonixWorkspace.workingDir), 'reasonix-permission-probe.txt')
    const command = 'printf permission-approved > reasonix-permission-probe.txt'
    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.REASONIX, 'reasonix-permission', command)] },
      { text: 'The approved file write finished.' },
    )
    await sendMessage(page, modelScript.prompt('Run the exact shell command in the scripted tool call.'))
    await modelScript.waitForSteps(1)

    const banner = page.getByTestId('control-banner').filter({ visible: true })
    await expect(banner).toContainText(command)
    await page.getByTestId('control-allow-btn').filter({ visible: true }).click()
    await modelScript.waitForSteps(2)
    await waitForAgentIdle(page)
    expect(readFileSync(file, 'utf8')).toBe('permission-approved')
    await expect(assistantBubbles(page).filter({ hasText: 'The approved file write finished.' }).first()).toBeVisible()
  })
})
