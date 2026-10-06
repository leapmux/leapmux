import { randomUUID } from 'node:crypto'
import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions } from '../agentSettings'
import { openAgentViaAPI } from '../helpers/api'
import { exerciseNativePermissionDecision } from '../helpers/nativePermission'
import { bashToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { openWorkspace } from '../helpers/ui'
import { createGitRepo } from '../helpers/worktree'
import { opencodeTest } from '../opencode-fixtures'
import { exerciseOpenCodeFamilyDenial } from './permissionDenial'
import { nativeContext } from './scenarios'

opencodeTest('asks before a shell command touches a file outside the working directory', async ({ authenticatedEmptyWorkspace, leapmuxServer, page, modelScript }) => {
  const parent = createTestDirectory('opencode-permission-')
  const workingDir = createGitRepo(parent, 'repo')
  const file = join(parent, 'opencode-permission-probe.txt')
  writeFileSync(file, 'remove this test file')
  await openAgentViaAPI(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, workingDir, agentOpenOptions(AgentProvider.OPENCODE))
  await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  // The path leaves the working directory, so OpenCode asks its external_directory permission.
  const command = 'rm ../opencode-permission-probe.txt'
  await exerciseNativePermissionDecision(context, {
    toolCall: bashToolCall(context.provider, 'opencode-permission', command),
    decision: 'allow',
    beforeDecision: async (banner) => {
      await expect(banner).toContainText(command)
      expect(existsSync(file)).toBe(true)
    },
    nativeProof: () => {
      expect(existsSync(file)).toBe(false)
    },
  })
})

opencodeTest('keeps exact outside-directory bytes after a native Deny decision', async ({ authenticatedEmptyWorkspace, leapmuxServer, page, modelScript }) => {
  const parent = createTestDirectory('opencode-denied-permission-')
  const workingDir = createGitRepo(parent, 'repo')
  const file = join(parent, 'opencode-denied-permission-probe.txt')
  const original = `KEEP_THE_OUTSIDE_FILE_${randomUUID()}\n`
  writeFileSync(file, original)
  await openAgentViaAPI(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, workingDir, agentOpenOptions(AgentProvider.OPENCODE))
  await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  // The path leaves the working directory, so OpenCode asks its external_directory permission.
  const command = 'rm ../opencode-denied-permission-probe.txt'
  await exerciseOpenCodeFamilyDenial(context, {
    toolCall: bashToolCall(context.provider, 'opencode-denied-removal', command),
    prompt: 'Run the exact shell command in the scripted tool call.',
    bannerText: command,
    expectUnchanged: () => expect(readFileSync(file, 'utf8')).toBe(original),
  })
})
