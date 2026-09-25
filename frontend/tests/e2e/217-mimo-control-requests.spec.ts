import type { Page } from '@playwright/test'
import type { WorkspaceFixture } from './helpers/workspace'
import { existsSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from './agentSettings'
import { openAgentViaAPI } from './helpers/api'
import { askUserQuestionToolCall, bashToolCall, mimoInteractiveBashToolCall, readToolCall } from './helpers/providerToolCalls'
import { createTestDirectory } from './helpers/runDirectory'
import { messageContents, openWorkspace, sendMessage, waitForAgentIdle, waitForControlBanner } from './helpers/ui'
import { expect, MIMO_E2E_SKIP_REASON, mimoTest } from './mimo-fixtures'

mimoTest.skip(!!MIMO_E2E_SKIP_REASON, MIMO_E2E_SKIP_REASON || '')

/** The row that states the answer the reader gave to a control request. */
function savedAnswer(page: Page) {
  return page.locator('[data-testid="control-response-text"]:visible')
}

interface Server {
  hubUrl: string
  adminToken: string
  workerId: string
}

/**
 * Open a MiMo agent in a directory that this test owns, with one file in it.
 *
 * MiMo's build agent runs every tool without asking, except a shell command that
 * deletes. That one always asks (`bash_delete`), so a deletion of the file is what
 * raises a permission request, and the file itself states whether the command ran.
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
    await waitForAgentIdle(page, 120_000)

    expect(existsSync(file)).toBe(false)
    await expect(messageContents(page).filter({ hasText: 'DELETED_AFTER_APPROVAL' }).first()).toBeVisible()
    // The transcript keeps the answer as MiMo's own option word.
    await expect(savedAnswer(page)).toHaveText('Allow once')
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
    await waitForAgentIdle(page, 120_000)

    expect(existsSync(file)).toBe(true)
    await expect(messageContents(page).filter({ hasText: 'Declined' }).first()).toBeVisible()
    await expect(savedAnswer(page)).toHaveText('Reject')
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
    await waitForAgentIdle(page, 120_000)
    expect(existsSync(file)).toBe(true)
  })

  // MiMo keeps an `always` answer for the patterns that its request states. A read
  // outside the project asks `external_directory` with the directory as that
  // pattern, so a second read in the same directory asks nothing.
  //
  // The directory must be outside EVERY git worktree: MiMo counts the whole worktree
  // as the project, and each E2E directory is inside this repository's worktree. A
  // fresh directory also keeps the stored answer of one run from covering the next.
  mimoTest('an always answer covers the next read in the same outside directory', async ({ authenticatedMiMoWorkspace, page, modelScript }) => {
    void authenticatedMiMoWorkspace
    const outside = mkdtempSync(join(tmpdir(), 'leapmux-mimo-always-'))
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
      await waitForAgentIdle(page, 120_000)

      await expect(page.locator('[data-testid="control-banner"]')).toHaveCount(0)
      await expect(savedAnswer(page)).toHaveCount(1)
      await expect(savedAnswer(page)).toHaveText('Always allow')
      await expect(messageContents(page).filter({ hasText: 'READ_BOTH_OUTSIDE_FILES' }).first()).toBeVisible()
    }
    finally {
      rmSync(outside, { recursive: true, force: true })
    }
  })
})

mimoTest.describe('MiMo Code questions', () => {
  const questions = [{
    question: 'Choose a style',
    header: 'Style',
    options: [
      { label: 'Alpha', description: 'Use the first style.' },
      { label: 'Beta', description: 'Use the second style.' },
    ],
  }]

  mimoTest('delivers the selected answer to the question tool', async ({ authenticatedMiMoWorkspace, page, modelScript }) => {
    void authenticatedMiMoWorkspace
    await modelScript.queue(
      { toolCalls: [askUserQuestionToolCall(AgentProvider.MIMO_CODE, 'style-question', questions)] },
      { text: 'Recorded the style.' },
    )
    await sendMessage(page, modelScript.prompt('Ask me which style to use.'))
    await modelScript.waitForSteps(1)

    const banner = page.getByTestId('control-banner').filter({ visible: true })
    await expect(banner).toContainText('Choose a style')
    await expect(banner.getByText('Use the second style.', { exact: true })).toBeVisible()
    await banner.getByTestId('question-option-Beta').click()
    await page.getByTestId('control-submit-btn').click()
    await expect(banner).toHaveCount(0)
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)

    // The question row reads the answer out of MiMo's own result, under the
    // question's header, and the saved answer states it too.
    await expect(messageContents(page).filter({ hasText: /Style\s*—\s*Beta/ }).first()).toBeVisible()
    await expect(savedAnswer(page)).toHaveText('Style: Beta')
    await expect(messageContents(page).filter({ hasText: 'Recorded the style.' }).first()).toBeVisible()
  })

  // MiMo takes one list of answers for each question: the chosen options of a
  // multi-select question, and the typed words of a question that the reader
  // answered in the composer.
  mimoTest('delivers several choices and typed words, one answer for each question', async ({ authenticatedMiMoWorkspace, page, modelScript }) => {
    void authenticatedMiMoWorkspace
    await modelScript.queue(
      {
        toolCalls: [askUserQuestionToolCall(AgentProvider.MIMO_CODE, 'pizza-questions', [
          {
            question: 'Choose the toppings',
            header: 'Toppings',
            multiSelect: true,
            options: [
              { label: 'Cheese', description: 'Add cheese.' },
              { label: 'Olives', description: 'Add olives.' },
              { label: 'Basil', description: 'Add basil.' },
            ],
          },
          {
            question: 'Name the pizza',
            header: 'Name',
            options: [
              { label: 'Margherita', description: 'The classic name.' },
              { label: 'Marinara', description: 'The name with no cheese.' },
            ],
          },
        ])],
      },
      { text: 'Recorded the pizza.' },
    )
    await sendMessage(page, modelScript.prompt('Ask me about the pizza.'))
    await modelScript.waitForSteps(1)

    const banner = page.getByTestId('control-banner').filter({ visible: true })
    await expect(banner).toContainText('Choose the toppings')
    await banner.getByTestId('question-option-Cheese').click()
    await banner.getByTestId('question-option-Basil').click()
    // A multi-select question stays on its page, so the reader moves on by hand.
    await page.getByTestId('control-pagination').locator('button').nth(1).click()
    await expect(banner).toContainText('Name the pizza')
    await page.getByTestId('composer-editor').locator('.ProseMirror').fill('Garden Special')
    const submit = page.getByTestId('control-submit-btn')
    await expect(submit).toBeEnabled()
    await submit.click()
    await expect(banner).toHaveCount(0)
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)

    await expect(messageContents(page).filter({ hasText: /Toppings\s*—\s*Cheese, Basil/ }).first()).toBeVisible()
    await expect(messageContents(page).filter({ hasText: /Name\s*—\s*Garden Special/ }).first()).toBeVisible()
    await expect(savedAnswer(page)).toContainText('Toppings: Cheese, Basil')
    await expect(savedAnswer(page)).toContainText('Name: Garden Special')
    await expect(messageContents(page).filter({ hasText: 'Recorded the pizza.' }).first()).toBeVisible()
  })

  // Stop dismisses the question. A dismissal stops MiMo's loop, as a rejected
  // permission does.
  mimoTest('dismisses the question without an answer', async ({ authenticatedMiMoWorkspace, page, modelScript }) => {
    void authenticatedMiMoWorkspace
    await modelScript.queue({ toolCalls: [askUserQuestionToolCall(AgentProvider.MIMO_CODE, 'style-question', questions)] })
    await sendMessage(page, modelScript.prompt('Ask me which style to use.'))
    await modelScript.waitForSteps()

    const banner = page.getByTestId('control-banner').filter({ visible: true })
    await expect(banner).toContainText('Choose a style')
    await page.getByTestId('control-stop-btn').click()
    await expect(banner).toHaveCount(0)
    await waitForAgentIdle(page, 120_000)
    await expect(savedAnswer(page)).toHaveText('Dismissed')
  })
})

mimoTest.describe('MiMo Code interactive commands', () => {
  // Nobody can type into a command that LeapMux runs, so the worker refuses the
  // request at once. The turn goes on: the model reads the refusal as the
  // command's output and answers.
  mimoTest('refuses an interactive command without blocking the turn', async ({ authenticatedMiMoWorkspace, page, modelScript }) => {
    void authenticatedMiMoWorkspace
    await modelScript.queue(
      { toolCalls: [mimoInteractiveBashToolCall('interactive-call', 'read -p "Name? " name; echo "hi $name"')] },
      { text: 'INTERACTIVE_REFUSED' },
    )
    await sendMessage(page, modelScript.prompt('Ask for my name in the shell.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)

    await expect(page.locator('[data-testid="control-banner"]')).toHaveCount(0)
    await expect(messageContents(page).filter({ hasText: 'LeapMux cannot run an interactive command' }).first()).toBeVisible()
    await expect(messageContents(page).filter({ hasText: 'INTERACTIVE_REFUSED' }).first()).toBeVisible()
  })
})
