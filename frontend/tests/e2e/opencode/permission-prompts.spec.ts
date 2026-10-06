import { randomUUID } from 'node:crypto'
import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from '../agentSettings'
import { openAgentViaAPI } from '../helpers/api'
import { bashToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { assistantBubbles, openWorkspace, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { createGitRepo } from '../helpers/worktree'
import { opencodeTest } from '../opencode-fixtures'
import { exerciseOpenCodeFamilyDenial } from './permissionDenial'

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

opencodeTest('keeps exact outside-directory bytes after a native Deny decision', async ({ authenticatedEmptyWorkspace, leapmuxServer, page, modelScript }) => {
  const parent = createTestDirectory('opencode-denied-permission-')
  const workingDir = createGitRepo(parent, 'repo')
  const file = join(parent, 'opencode-denied-permission-probe.txt')
  const original = `KEEP_THE_OUTSIDE_FILE_${randomUUID()}\n`
  writeFileSync(file, original)
  await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, workingDir, {
    agentProvider: AgentProvider.OPENCODE,
    ...agentOpenOptions(agentSettings(AgentProvider.OPENCODE)),
  })
  await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
  // The path leaves the working directory, so OpenCode asks its external_directory permission.
  const command = 'rm ../opencode-denied-permission-probe.txt'
  await exerciseOpenCodeFamilyDenial({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId, provider: AgentProvider.OPENCODE }, {
    toolCall: bashToolCall(AgentProvider.OPENCODE, 'opencode-denied-removal', command),
    prompt: 'Run the exact shell command in the scripted tool call.',
    bannerText: command,
    expectUnchanged: () => expect(readFileSync(file, 'utf8')).toBe(original),
  })
})
