import type { Page } from '@playwright/test'
import type { WorkspaceFixture } from '../helpers/workspace'
import { execFileSync } from 'node:child_process'
import { existsSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { expectTurnEndedAfter } from '../helpers/modelScriptFixture'
import { exerciseNativePermissionDecision } from '../helpers/nativePermission'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { bashToolCall, readToolCall } from '../helpers/providerToolCalls'
import { deliberateWorkingDir, newProviderWorkingDir } from '../helpers/providerWorkingDir'
import { createTestDirectory } from '../helpers/runDirectory'
import { hubSpawnEnv } from '../helpers/server'
import { answerControl, controlActions, controlButton, enterControlFeedback, expectNoControlBanner, messageContents, openWorkspace, savedControlAnswer, sendMessage, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { createGitRepo } from '../helpers/worktree'
import { mimoTest } from '../mimo-fixtures'
import { MIMO_AGENT, nativeContext } from './scenarios'

interface Server {
  hubUrl: string
  adminToken: string
  workerId: string
}

/**
 * Open a MiMo agent in a private directory that contains one file.
 * The build agent runs its tools without approval, except shell deletion commands.
 * A deletion raises the native `bash_delete` request. The file's presence proves whether the command ran.
 */
async function openAgentWithFile(page: Page, server: Server, workspace: WorkspaceFixture, prefix: string): Promise<string> {
  const directory = newProviderWorkingDir(MIMO_AGENT, prefix)
  writeFileSync(join(directory, 'doomed.txt'), 'delete me\n')
  await openProviderAgent(server, workspace.workspaceId, MIMO_AGENT, { workingDir: directory })
  await openWorkspace(page, workspace.workspaceId)
  return join(directory, 'doomed.txt')
}

mimoTest.describe('MiMo Code permission requests', () => {
  mimoTest('an approved deletion runs, and the saved answer states the option', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const file = await openAgentWithFile(page, leapmuxServer, authenticatedEmptyWorkspace, 'mimo-permission-allow-')
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    await exerciseNativePermissionDecision(context, {
      toolCall: bashToolCall(context.provider, 'delete-call', 'rm -f doomed.txt'),
      decision: 'allow',
      beforeDecision: async (banner) => {
        await expect(banner).toContainText('rm -f doomed.txt')
        // MiMo asks every delete, and reads an `always` answer to one as `once`. So
        // the control actions offer no scope that MiMo would not keep.
        await expect(page.getByRole('radiogroup', { name: 'Allow scope' })).toHaveCount(0)
        await expect(page.getByTestId('control-decision-always')).toHaveCount(0)
        expect(existsSync(file)).toBe(true)
      },
      nativeProof: () => {
        expect(existsSync(file)).toBe(false)
      },
      viewProof: async () => {
        await expectNoControlBanner(page)
        // The transcript keeps the answer as MiMo's own option word.
        await expect(savedControlAnswer(page)).toHaveText('Allow once')
      },
    })
  })

  // A plain rejection stops MiMo's loop, so MiMo does not ask the model again.
  mimoTest('a rejected deletion does not run, and the call reads as declined', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const file = await openAgentWithFile(page, leapmuxServer, authenticatedEmptyWorkspace, 'mimo-permission-reject-')
    const start = await modelScript.queue({ toolCalls: [bashToolCall(AgentProvider.MIMO_CODE, 'delete-call', 'rm -f doomed.txt')] })
    await sendMessage(page, modelScript.prompt('Delete doomed.txt.'))
    await modelScript.waitForSteps(start + 1)

    await waitForControlBanner(page)
    expect(existsSync(file)).toBe(true)
    await answerControl(page, 'deny')
    await expectNoControlBanner(page)
    await waitForAgentIdle(page)

    expect(existsSync(file)).toBe(true)
    await expectTurnEndedAfter(modelScript, start + 1)
    await expect(messageContents(page).filter({ hasText: 'Declined' }).first()).toBeVisible()
    await expect(savedControlAnswer(page)).toHaveText('Reject')
  })

  // A rejection WITH words is a correction: MiMo hands the words to the model and
  // its loop continues.
  mimoTest('a rejection with feedback reaches the model as the reason', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const file = await openAgentWithFile(page, leapmuxServer, authenticatedEmptyWorkspace, 'mimo-permission-feedback-')
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    const reason = 'Keep the file for the audit'
    await exerciseNativePermissionDecision(context, {
      toolCall: bashToolCall(context.provider, 'delete-call', 'rm -f doomed.txt'),
      decision: 'deny',
      beforeDecision: async (banner) => {
        await expect(banner).toContainText('rm -f doomed.txt')
        expect(existsSync(file)).toBe(true)
        await enterControlFeedback(page, reason)
        await expect(controlButton(page, 'deny')).toHaveText('Send feedback')
      },
      // The model reads the reason in the request that continues the loop.
      nativeProof: (request) => {
        expect(JSON.stringify(request.body)).toContain(reason)
        expect(existsSync(file)).toBe(true)
      },
      viewProof: () => expectNoControlBanner(page),
    })
  })

  // MiMo keeps an `always` answer for the patterns that its request states. A read
  // outside the project asks `external_directory` with the directory as that
  // pattern, so a second read in the same directory asks nothing.
  mimoTest('an always answer covers the next read in the same outside directory', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
    // The rule of MiMo makes a plain directory of the run, whose git worktree is the whole LeapMux checkout, and the
    // checkout also holds the sibling directory below.
    const workingDir = deliberateWorkingDir(
      createGitRepo(createTestDirectory('mimo-outside-project-'), 'repo'),
      'MiMo treats the whole git worktree of its directory as its project. A repository of its own keeps the sibling directory of the run outside the project.',
    )
    const outside = createTestDirectory('mimo-always-outside-')
    const gitEnv = hubSpawnEnv(leapmuxServer.agentEnv)
    expect(execFileSync('git', ['rev-parse', '--show-toplevel'], { cwd: workingDir, env: gitEnv, encoding: 'utf8' }).trim()).toBe(workingDir)
    const outsideProbe = (() => {
      try {
        return execFileSync('git', ['rev-parse', '--show-toplevel'], { cwd: outside, env: gitEnv, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] }).trim()
      }
      catch {
        return null
      }
    })()
    expect(outsideProbe).toBeNull()
    await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, MIMO_AGENT, { workingDir })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    try {
      writeFileSync(join(outside, 'first.txt'), 'FIRST_OUTSIDE_FILE\n')
      writeFileSync(join(outside, 'second.txt'), 'SECOND_OUTSIDE_FILE\n')
      const start = await modelScript.queue(
        { toolCalls: [readToolCall(AgentProvider.MIMO_CODE, 'outside-first', join(outside, 'first.txt'))] },
        { toolCalls: [readToolCall(AgentProvider.MIMO_CODE, 'outside-second', join(outside, 'second.txt'))] },
        { text: 'READ_BOTH_OUTSIDE_FILES' },
      )
      await sendMessage(page, modelScript.prompt('Read the two files outside the project.'))
      await modelScript.waitForSteps(start + 1)

      await waitForControlBanner(page)
      // The scope pills sit in the control actions of the composer, not in the banner.
      const scope = controlActions(page).getByRole('radiogroup', { name: 'Allow scope' })
      await scope.getByRole('radio', { name: 'Always' }).click()
      await expect(scope.getByRole('radio', { name: 'Always' })).toBeChecked()
      await answerControl(page, 'allow')
      await expectNoControlBanner(page)
      // The second read asks nothing, so the turn reaches its answer with no further decision.
      await modelScript.waitForSteps(start + 3)
      await waitForAgentIdle(page)

      await expectNoControlBanner(page)
      await expect(savedControlAnswer(page)).toHaveCount(1)
      await expect(savedControlAnswer(page)).toHaveText('Always allow')
      await expect(messageContents(page).filter({ hasText: 'READ_BOTH_OUTSIDE_FILES' }).first()).toBeVisible()
      // Each read returned its own file, so the second read ran under the kept answer.
      expect(nativeToolResult(await modelScript.requestAt(start + 1), 'outside-first')).toContain('FIRST_OUTSIDE_FILE')
      expect(nativeToolResult(await modelScript.requestAt(start + 2), 'outside-second')).toContain('SECOND_OUTSIDE_FILE')
    }
    finally {
      rmSync(outside, { recursive: true, force: true })
    }
  })
})
