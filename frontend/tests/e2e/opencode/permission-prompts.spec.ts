import { randomUUID } from 'node:crypto'
import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { expect } from '@playwright/test'
import { exerciseNativePermissionDecision, exerciseNativePermissionReason, exerciseRememberedAllow } from '../helpers/nativePermission'
import { bashToolCall } from '../helpers/providerToolCalls'
import { savedControlAnswer } from '../helpers/ui'
import { opencodeTest } from '../opencode-fixtures'
import { outsideRemovalCommand, projectBesideProbe, withOutsideProjectAgent } from './outsideProject'
import { exerciseOpenCodeFamilyDenial } from './permissionDenial'

opencodeTest('asks before a shell command touches a file outside the working directory', async ({ authenticatedEmptyWorkspace, leapmuxServer, page, modelScript }) => {
  const { workingDir, file } = projectBesideProbe('opencode-permission-', 'remove this test file')
  await withOutsideProjectAgent({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: authenticatedEmptyWorkspace.workspaceId,
  }, workingDir, async (context) => {
  // The private profile requires approval for the outside directory before the removal.
    const command = outsideRemovalCommand(file, 'OPENCODE_PERMISSION_')
    await exerciseNativePermissionDecision(context, {
      toolCall: bashToolCall(context.provider, 'opencode-permission', command),
      decision: 'allow',
      beforeDecision: async (banner) => {
        await expect(banner).toContainText(dirname(file))
        expect(existsSync(file)).toBe(true)
      },
      nativeProof: () => {
        expect(existsSync(file)).toBe(false)
      },
      // The saved row reads the name of OpenCode's own `once` option.
      viewProof: () => expect(savedControlAnswer(page)).toHaveText('Allowed'),
    })
  })
})

opencodeTest('keeps exact outside-directory bytes after a native Deny decision', async ({ authenticatedEmptyWorkspace, leapmuxServer, page, modelScript }) => {
  const original = `KEEP_THE_OUTSIDE_FILE_${randomUUID()}\n`
  const { workingDir, file } = projectBesideProbe('opencode-denied-permission-', original)
  await withOutsideProjectAgent({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: authenticatedEmptyWorkspace.workspaceId,
  }, workingDir, async (context) => {
  // The private profile requires approval for the outside directory before the removal.
    const command = outsideRemovalCommand(file, 'OPENCODE_PERMISSION_')
    await exerciseOpenCodeFamilyDenial(context, {
      toolCall: bashToolCall(context.provider, 'opencode-denied-removal', command),
      prompt: 'Run the exact shell command in the scripted tool call.',
      bannerText: dirname(file),
      expectUnchanged: () => expect(readFileSync(file, 'utf8')).toBe(original),
    })
  })
})

// The native permission reply carries feedback as the refused tool result, so the same turn can continue.
opencodeTest('sends the reader\'s typed refusal reason in the native permission reply', async ({ authenticatedEmptyWorkspace, leapmuxServer, page, modelScript }) => {
  const original = `KEEP_THE_OUTSIDE_FILE_${randomUUID()}\n`
  const { workingDir, file } = projectBesideProbe('opencode-reason-permission-', original)
  await withOutsideProjectAgent({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: authenticatedEmptyWorkspace.workspaceId,
  }, workingDir, async (context) => {
    const command = outsideRemovalCommand(file, 'OPENCODE_PERMISSION_')
    await exerciseNativePermissionReason(context, {
      toolCall: bashToolCall(context.provider, 'opencode-reason-removal', command),
      route: 'native-reply',
      afterRefusal: 'continues',
      beforeDecision: banner => expect(banner).toContainText(dirname(file)),
      expectNotRun: () => expect(readFileSync(file, 'utf8')).toBe(original),
      viewProof: () => expect(savedControlAnswer(page)).toHaveText('Denied'),
    })
  })
})

// OpenCode keeps an always answer for the outside directory that its request states, so a later removal in the same
// directory asks nothing.
opencodeTest('an always answer covers a later removal in the same outside directory', async ({ authenticatedEmptyWorkspace, leapmuxServer, page, modelScript }) => {
  const { workingDir, file: first } = projectBesideProbe('opencode-always-permission-', 'remove the first file')
  const second = join(dirname(first), 'opencode-always-second.txt')
  writeFileSync(second, 'remove the second file')
  await withOutsideProjectAgent({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: authenticatedEmptyWorkspace.workspaceId,
  }, workingDir, async (context) => {
    await exerciseRememberedAllow(context, {
      scope: 'Project',
      firstCall: bashToolCall(context.provider, 'opencode-always-first', outsideRemovalCommand(first, 'OPENCODE_ALWAYS_FIRST_')),
      secondCall: bashToolCall(context.provider, 'opencode-always-second', outsideRemovalCommand(second, 'OPENCODE_ALWAYS_SECOND_')),
      beforeDecision: () => {
        expect(existsSync(first)).toBe(true)
        expect(existsSync(second)).toBe(true)
      },
      firstProof: () => {
        expect(existsSync(first)).toBe(false)
        expect(existsSync(second)).toBe(true)
      },
      secondProof: () => expect(existsSync(second)).toBe(false),
      viewProof: () => expect(savedControlAnswer(page)).toHaveText('Always allowed'),
    })
  })
})
