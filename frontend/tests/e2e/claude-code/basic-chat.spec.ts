import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { claudeTest } from '../claude-fixtures'
import { test } from '../fixtures'
import { exerciseBasicChat } from '../helpers/nativeConversation'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, expectAssistantAnswer } from '../helpers/ui'

test.describe('Workspace Chat', () => {
  test('should create workspace, open agent, and receive response from Claude', async ({ page, authenticatedWorkspace, modelScript }) => {
    // The fixture creates the workspace and its agent tab.
    // Wait for the Milkdown editor to be ready.
    const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // OpenAgent returns STARTING before the native process is ready. Wait for that overlay before sending ordinary chat input.
    await expect(page.getByText(/^Starting /)).not.toBeVisible()

    await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })

    // Send a message to Claude via the rich text editor.
    await editor.click()
    await page.keyboard.type(modelScript.prompt(ARITHMETIC_PROMPT))
    await page.keyboard.press('Meta+Enter')

    // The editor clears after it accepts the message.
    await expect(editor).toHaveText('')

    // Wait for Claude's response to appear in an assistant message bubble.
    await expectAssistantAnswer(page)
  })
})

claudeTest('ends an actual native chat turn and restores its answer after reload', async ({ authenticatedClaudeWorkspace, page, modelScript }) => {
  void authenticatedClaudeWorkspace
  await exerciseBasicChat({ page, modelScript, provider: AgentProvider.CLAUDE_CODE })
})
