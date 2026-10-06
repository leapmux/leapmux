import type { Page } from '@playwright/test'
import type { WorkspaceFixture } from '../helpers/workspace'
import { execFileSync } from 'node:child_process'
import { existsSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from '../agentSettings'
import { openAgentViaAPI } from '../helpers/api'
import { bashToolCall, readToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { hubSpawnEnv } from '../helpers/server'
import { messageContents, openWorkspace, savedControlAnswer, sendMessage, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'

import { createGitRepo } from '../helpers/worktree'
import { mimoTest } from '../mimo-fixtures'

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
  const directory = createTestDirectory(prefix)
  writeFileSync(join(directory, 'doomed.txt'), 'delete me\n')
  await openAgentViaAPI(server.hubUrl, server.adminToken, server.workerId, workspace.workspaceId, directory, {
    agentProvider: AgentProvider.MIMO_CODE,
    ...agentOpenOptions(agentSettings(AgentProvider.MIMO_CODE)),
  })
  await openWorkspace(page, workspace.workspaceId)
  return join(directory, 'doomed.txt')
}

mimoTest.describe('MiMo Code permission requests', () => {
  mimoTest('an approved deletion runs, and the saved answer states the option', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const file = await openAgentWithFile(page, leapmuxServer, authenticatedEmptyWorkspace, 'mimo-permission-allow-')
    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.MIMO_CODE, 'delete-call', 'rm -f doomed.txt')] },
      { text: 'DELETED_AFTER_APPROVAL' },
    )
    await sendMessage(page, modelScript.prompt('Delete doomed.txt.'))
    await modelScript.waitForSteps(1)

    const banner = await waitForControlBanner(page)
    await expect(banner).toContainText('rm -f doomed.txt')
    // MiMo asks every delete, and reads an `always` answer to one as `once`. So
    // the banner offers no scope that MiMo would not keep.
    await expect(page.getByRole('radiogroup', { name: 'Allow scope' })).toHaveCount(0)
    await expect(page.getByTestId('control-decision-always')).toHaveCount(0)
    expect(existsSync(file)).toBe(true)
    await page.getByTestId('control-allow-btn').click()
    await expect(page.locator('[data-testid="control-banner"]')).not.toBeVisible()
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    expect(existsSync(file)).toBe(false)
    await expect(messageContents(page).filter({ hasText: 'DELETED_AFTER_APPROVAL' }).first()).toBeVisible()
    // The transcript keeps the answer as MiMo's own option word.
    await expect(savedControlAnswer(page)).toHaveText('Allow once')
  })

  // A plain rejection stops MiMo's loop, so the model is not asked again.
  mimoTest('a rejected deletion does not run, and the call reads as declined', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const file = await openAgentWithFile(page, leapmuxServer, authenticatedEmptyWorkspace, 'mimo-permission-reject-')
    await modelScript.queue({ toolCalls: [bashToolCall(AgentProvider.MIMO_CODE, 'delete-call', 'rm -f doomed.txt')] })
    await sendMessage(page, modelScript.prompt('Delete doomed.txt.'))
    await modelScript.waitForSteps()

    await waitForControlBanner(page)
    await page.getByTestId('control-deny-btn').click()
    await expect(page.locator('[data-testid="control-banner"]')).not.toBeVisible()
    await waitForAgentIdle(page)

    expect(existsSync(file)).toBe(true)
    await expect(messageContents(page).filter({ hasText: 'Declined' }).first()).toBeVisible()
    await expect(savedControlAnswer(page)).toHaveText('Reject')
  })

  // A rejection WITH words is a correction: MiMo hands the words to the model and
  // its loop continues.
  mimoTest('a rejection with feedback reaches the model as the reason', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const file = await openAgentWithFile(page, leapmuxServer, authenticatedEmptyWorkspace, 'mimo-permission-feedback-')
    await modelScript.queue({ toolCalls: [bashToolCall(AgentProvider.MIMO_CODE, 'delete-call', 'rm -f doomed.txt')] })
    await modelScript.rule({
      name: 'the model reads the rejection feedback',
      when: { body: 'Keep the file for the audit' },
      respond: { text: 'FEEDBACK_RECEIVED' },
      once: true,
    })
    await sendMessage(page, modelScript.prompt('Delete doomed.txt.'))
    await modelScript.waitForSteps()

    await waitForControlBanner(page)
    const editor = page.getByTestId('composer-editor').locator('.ProseMirror')
    await editor.fill('Keep the file for the audit')
    const reject = page.getByTestId('control-deny-btn')
    await expect(reject).toHaveText('Send feedback')
    await reject.click()
    await expect(page.locator('[data-testid="control-banner"]')).not.toBeVisible()
    await expect(messageContents(page).filter({ hasText: 'FEEDBACK_RECEIVED' }).first()).toBeVisible()
    await waitForAgentIdle(page)
    expect(existsSync(file)).toBe(true)
  })

  // MiMo keeps an `always` answer for the patterns that its request states. A read
  // outside the project asks `external_directory` with the directory as that
  // pattern, so a second read in the same directory asks nothing.
  //
  // MiMo treats the full Git worktree as its project. A sibling directory stays
  // outside this agent's fresh repository and inside the private E2E run.
  mimoTest('an always answer covers the next read in the same outside directory', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
    const workingDir = createGitRepo(createTestDirectory('mimo-outside-project-'), 'repo')
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
    await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, workingDir, {
      agentProvider: AgentProvider.MIMO_CODE,
      ...agentOpenOptions(agentSettings(AgentProvider.MIMO_CODE)),
    })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    try {
      writeFileSync(join(outside, 'first.txt'), 'FIRST_OUTSIDE_FILE\n')
      writeFileSync(join(outside, 'second.txt'), 'SECOND_OUTSIDE_FILE\n')
      await modelScript.queue(
        { toolCalls: [readToolCall(AgentProvider.MIMO_CODE, 'outside-first', join(outside, 'first.txt'))] },
        { toolCalls: [readToolCall(AgentProvider.MIMO_CODE, 'outside-second', join(outside, 'second.txt'))] },
        { text: 'READ_BOTH_OUTSIDE_FILES' },
      )
      await sendMessage(page, modelScript.prompt('Read the two files outside the project.'))
      await modelScript.waitForSteps(1)

      await waitForControlBanner(page)
      const scope = page.getByRole('radiogroup', { name: 'Allow scope' })
      await scope.getByRole('radio', { name: 'Always' }).click()
      await expect(scope.getByRole('radio', { name: 'Always' })).toBeChecked()
      await page.getByTestId('control-allow-btn').click()
      await expect(page.locator('[data-testid="control-banner"]')).not.toBeVisible()
      await modelScript.waitForSteps()
      await waitForAgentIdle(page)

      await expect(page.locator('[data-testid="control-banner"]')).toHaveCount(0)
      await expect(savedControlAnswer(page)).toHaveCount(1)
      await expect(savedControlAnswer(page)).toHaveText('Always allow')
      await expect(messageContents(page).filter({ hasText: 'READ_BOTH_OUTSIDE_FILES' }).first()).toBeVisible()
    }
    finally {
      rmSync(outside, { recursive: true, force: true })
    }
  })
})
