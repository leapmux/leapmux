import type { ProviderWorkingDir } from '../helpers/providerWorkingDir'
import { randomUUID } from 'node:crypto'
import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { basename, dirname, join } from 'node:path'
import { expect } from '@playwright/test'
import { exerciseNativePermissionDecision, exerciseNativePermissionReason, exerciseRememberedAllow } from '../helpers/nativePermission'
import { bashToolCall } from '../helpers/providerToolCalls'
import { deliberateWorkingDir } from '../helpers/providerWorkingDir'
import { createTestDirectory } from '../helpers/runDirectory'
import { openWorkspace, savedControlAnswer } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { createGitRepo } from '../helpers/worktree'
import { opencodeTest } from '../opencode-fixtures'
import { exerciseOpenCodeFamilyDenial } from './permissionDenial'
import { nativeContext, OPENCODE_AGENT } from './scenarios'

/**
 * Write a probe file that holds `content` into a new directory of the run whose name starts with `prefix`, and make a
 * working directory beside it. Return both. The name of the probe file is `<prefix>probe.txt`.
 *
 * OpenCode treats the whole git worktree of its directory as its project, and asks its `external_directory`
 * permission only for a path outside that project. The rule of OpenCode makes a plain directory of the run, whose
 * worktree is the LeapMux checkout, and the checkout holds the probe file too. So the working directory is the root of
 * a repository of its own, and the probe file sits in its parent, outside the project.
 */
function projectBesideProbe(prefix: string, content: string): { workingDir: ProviderWorkingDir, file: string } {
  const parent = createTestDirectory(prefix)
  const file = join(parent, `${prefix}probe.txt`)
  writeFileSync(file, content)
  const workingDir = deliberateWorkingDir(
    createGitRepo(parent, 'repo'),
    'OpenCode asks its external_directory permission only for a path outside the git worktree of its directory, so the directory is a repository of its own and the probe file sits in its parent.',
  )
  return { workingDir, file }
}

opencodeTest('asks before a shell command touches a file outside the working directory', async ({ authenticatedEmptyWorkspace, leapmuxServer, page, modelScript }) => {
  const { workingDir, file } = projectBesideProbe('opencode-permission-', 'remove this test file')
  await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, OPENCODE_AGENT, { workingDir })
  await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  // The path leaves the working directory, so OpenCode asks its external_directory permission.
  const command = `rm ../${basename(file)}`
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
    // The saved row reads the name of OpenCode's own `once` option.
    viewProof: () => expect(savedControlAnswer(page)).toHaveText('Allow once'),
  })
})

opencodeTest('keeps exact outside-directory bytes after a native Deny decision', async ({ authenticatedEmptyWorkspace, leapmuxServer, page, modelScript }) => {
  const original = `KEEP_THE_OUTSIDE_FILE_${randomUUID()}\n`
  const { workingDir, file } = projectBesideProbe('opencode-denied-permission-', original)
  await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, OPENCODE_AGENT, { workingDir })
  await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  // The path leaves the working directory, so OpenCode asks its external_directory permission.
  const command = `rm ../${basename(file)}`
  await exerciseOpenCodeFamilyDenial(context, {
    toolCall: bashToolCall(context.provider, 'opencode-denied-removal', command),
    prompt: 'Run the exact shell command in the scripted tool call.',
    bannerText: command,
    expectUnchanged: () => expect(readFileSync(file, 'utf8')).toBe(original),
  })
})

// The ACP reply selects an option, and an option carries no text. OpenCode ends the turn after a refusal, so the
// reason follows as the reader's next message, which opens a turn of its own.
opencodeTest('sends the reader\'s typed refusal reason as the next message', async ({ authenticatedEmptyWorkspace, leapmuxServer, page, modelScript }) => {
  const original = `KEEP_THE_OUTSIDE_FILE_${randomUUID()}\n`
  const { workingDir, file } = projectBesideProbe('opencode-reason-permission-', original)
  await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, OPENCODE_AGENT, { workingDir })
  await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  const command = `rm ../${basename(file)}`
  await exerciseNativePermissionReason(context, {
    toolCall: bashToolCall(context.provider, 'opencode-reason-removal', command),
    route: 'next-message',
    afterRefusal: 'ends',
    beforeDecision: banner => expect(banner).toContainText(command),
    expectNotRun: () => expect(readFileSync(file, 'utf8')).toBe(original),
    viewProof: () => expect(savedControlAnswer(page)).toHaveText('Reject'),
  })
})

// OpenCode keeps an always answer for the outside directory that its request states, so a later removal in the same
// directory asks nothing.
opencodeTest('an always answer covers a later removal in the same outside directory', async ({ authenticatedEmptyWorkspace, leapmuxServer, page, modelScript }) => {
  const { workingDir, file: first } = projectBesideProbe('opencode-always-permission-', 'remove the first file')
  const second = join(dirname(first), 'opencode-always-second.txt')
  writeFileSync(second, 'remove the second file')
  await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, OPENCODE_AGENT, { workingDir })
  await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  await exerciseRememberedAllow(context, {
    scope: 'Always',
    firstCall: bashToolCall(context.provider, 'opencode-always-first', `rm ../${basename(first)}`),
    secondCall: bashToolCall(context.provider, 'opencode-always-second', `rm ../${basename(second)}`),
    beforeDecision: () => {
      expect(existsSync(first)).toBe(true)
      expect(existsSync(second)).toBe(true)
    },
    firstProof: () => {
      expect(existsSync(first)).toBe(false)
      expect(existsSync(second)).toBe(true)
    },
    secondProof: () => expect(existsSync(second)).toBe(false),
    viewProof: () => expect(savedControlAnswer(page)).toHaveText('Always allow'),
  })
})
