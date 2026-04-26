import { expect, test } from './fixtures'
import { assistantBubbles, firstAssistantBubble, waitForAgentIdle } from './helpers/ui'

/**
 * Smoke test for end-to-end chat rendering: user input → real LLM response →
 * rendered bubbles. The bubble component itself is exhaustively unit-tested
 * in `tests/unit/components/MessageBubble.test.tsx` (over 30 cases covering
 * thinking, todos, tools, attachments, edits, etc.). This e2e exercises the
 * remaining integration: the editor send path, the WebSocket/RPC delivery
 * to a real Claude agent, the streaming-to-rendered transition, and the
 * markdown HTML output that only Shiki + jsdom-incompatible CSS can verify.
 */

test.describe('Chat Message Rendering', () => {
  test('user message renders as human text and assistant reply renders as markdown', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()
    await page.keyboard.type('What is 2+2? Reply with just the number.')
    await page.keyboard.press('Meta+Enter')

    await expect(firstAssistantBubble(page)).toBeVisible()
    await waitForAgentIdle(page)

    // User bubble: shows the human text, NOT the raw JSON envelope.
    const userBubble = page.locator('[data-testid="message-bubble"][data-role="user"]').first()
    const userContent = userBubble.locator('[data-testid="message-content"]')
    await expect(userContent).toContainText('What is 2+2?')
    await expect(userContent).not.toContainText('{"content":')

    // Assistant bubble: rendered as HTML markdown (at least one <p>),
    // not raw text.
    const assistantBubble = assistantBubbles(page).filter({
      has: page.locator('[data-testid="message-content"] p'),
    }).first()
    const assistantContent = assistantBubble.locator('[data-testid="message-content"]')
    const paragraphs = await assistantContent.locator('p').count()
    expect(paragraphs).toBeGreaterThan(0)
  })
})
