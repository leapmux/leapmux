import { existsSync, readdirSync, readFileSync } from 'node:fs'

import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { bashToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, messageBubbles, openWorkspace, sendMessage, visibleControlBanner, waitForAgentIdle } from '../helpers/ui'

import { kiroTest, openKiroAgent } from '../kiro-fixtures'

const PROVIDER = AgentProvider.KIRO

/** The text of a file, or '' for a file that does not exist. */
function readIfPresent(path: string): string {
  return existsSync(path) ? readFileSync(path, 'utf8') : ''
}

/**
 * The permission rules that Kiro keeps for each workspace, under its own HOME, by
 * the directory of the workspace: `.kiro/workspace-roots/<hash>/permissions.yaml`.
 */
function workspaceRuleFiles(home: string): Record<string, string> {
  const roots = join(home, '.kiro', 'workspace-roots')
  if (!existsSync(roots))
    return {}
  return Object.fromEntries(readdirSync(roots).map(hash => [hash, readIfPresent(join(roots, hash, 'permissions.yaml'))]))
}

kiroTest.describe('Kiro control requests', () => {
  // Kiro's own rules ask before a command that writes a file. A rejection that
  // carries a reason puts it in Kiro's own `_meta.kiro.rejectionReason`, and Kiro
  // hands the reason to the model inside the same turn.
  kiroTest('approves one command and rejects the next with a reason the turn reads', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openKiroAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const approved = join(workingDir, 'approved.txt')
    const rejected = join(workingDir, 'rejected.txt')

    await modelScript.queue(
      { toolCalls: [bashToolCall(PROVIDER, 'kiro-approved', `printf approved > ${approved}`)] },
      { toolCalls: [bashToolCall(PROVIDER, 'kiro-rejected', `printf rejected > ${rejected}`)] },
      { text: 'I read the reason and stopped.' },
    )
    await sendMessage(page, modelScript.prompt('Create the two scripted files.'))
    await modelScript.waitForSteps(1)
    const banner = visibleControlBanner(page)
    await expect(banner).toContainText(`printf approved > ${approved}`)
    await page.getByTestId('control-allow-btn').filter({ visible: true }).click()

    await modelScript.waitForSteps(2)
    await expect(banner).toContainText(`printf rejected > ${rejected}`)
    expect(existsSync(approved)).toBe(true)
    const editor = page.getByTestId('composer-editor').locator('.ProseMirror')
    await editor.fill('Do not create the second file.')
    await page.keyboard.press('Meta+Enter')
    await expect(banner).toHaveCount(0)
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    const status = await modelScript.status()
    expect(JSON.stringify(status.requests.at(-1)?.body)).toContain('Do not create the second file.')
    await expect(messageBubbles(page).filter({ hasText: 'Sent feedback:' }).filter({ hasText: 'Do not create the second file.' }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'I read the reason and stopped.' })).toBeVisible()
    expect(existsSync(rejected)).toBe(false)
  })

  // Kiro keeps Always Allow in the session unless the reply supplies a wider scope.
  // The Workspace pill selects that wider scope. The Worker sends native `always-accept` with the requested scope.
  // The same command then runs without another request. Kiro keeps session rules in memory.
  // It writes workspace rules to the workspace rule file, which proves the selected scope.
  kiroTest('keeps an always-allow for the workspace', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openKiroAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const marker = join(workingDir, 'always.txt')
    const command = `printf always >> ${marker}`
    const home = leapmuxServer.agentEnv.HOME!
    const userRuleFile = join(home, '.kiro', 'settings', 'permissions.yaml')
    const workspaceRulesBefore = workspaceRuleFiles(home)
    const userRulesBefore = readIfPresent(userRuleFile)

    await modelScript.queue(
      { toolCalls: [bashToolCall(PROVIDER, 'kiro-always-1', command)] },
      { toolCalls: [bashToolCall(PROVIDER, 'kiro-always-2', command)] },
      { text: 'Both ran.' },
    )
    await sendMessage(page, modelScript.prompt('Run the scripted command twice.'))
    await modelScript.waitForSteps(1)
    const banner = visibleControlBanner(page)
    await expect(banner).toContainText(command)
    // The scope pills sit in the control actions of the composer, not in the banner.
    await page.getByTestId('control-actions').filter({ visible: true }).getByRole('radio', { name: 'Workspace', exact: true }).click()
    await page.getByTestId('control-allow-btn').filter({ visible: true }).click()
    await expect(banner).toHaveCount(0)

    // The second call runs under the rule, with no request of its own.
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expect(assistantBubbles(page).filter({ hasText: 'Both ran.' })).toBeVisible()
    await expect(messageBubbles(page).filter({ hasText: 'Always allow in this workspace' }).first()).toBeVisible()
    expect(readFileSync(marker, 'utf8'), 'both commands ran').toBe('alwaysalways')

    // A session rule writes no file, and a rule for the user writes the rule file of
    // the user. The workspace of this test is new, so its rule file is the one file
    // that changed.
    const changed = Object.entries(workspaceRuleFiles(home)).filter(([hash, rules]) => rules !== (workspaceRulesBefore[hash] ?? ''))
    expect(changed, 'Kiro keeps the rule for the workspace').toHaveLength(1)
    expect(changed[0]?.[1]).toContain('allow')
    expect(readIfPresent(userRuleFile), 'Kiro keeps no rule for the user').toBe(userRulesBefore)
  })
})
