import type { Page } from '@playwright/test'
import type { ModelScript } from './helpers/modelScriptFixture'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { test } from './fixtures'
import {
  expectAtBottom,
  expectRowHeld,
  markedParagraphs,
  readScrollHeight,
  requireTopRow,
  sendLongTurn,
  waitForScrollRest,
  waitForThinkingIndicator,
  watchRow,
  wheelChatUp,
} from './helpers/chatScrollStability'
import { updateTodosToolCall } from './helpers/providerToolCalls'
import { chatScrollContainer, sendMessage, waitForAgentIdle, waitForAgentStarted } from './helpers/ui'

/**
 * The chat view keeps a reader who does not follow the tail in place while the agent's output arrives, and keeps a
 * follower at the bottom. An answer arrives as whole messages: Claude Code runs with partial messages off, so no row
 * grows while the model writes it.
 *
 * Each case holds the answer at a gate of the mock model, puts the reader where the case needs, then releases the
 * answer. A watch samples the reader's row in every frame between the release and the rest after the turn, so a jump
 * that a later frame corrects fails too.
 */

const VIEWPORT = { width: 1024, height: 600 }

/** About 155 characters: two lines of a paragraph at the width of the test. */
const FILLER = 'The reader keeps this paragraph in view while the agent writes more below it. '.repeat(2).trim()

/** The earlier answer that the reader reads: much taller than the view. */
const EARLIER_ANSWER = markedParagraphs('E', 24, FILLER)

/** The tall answer that arrives while the reader reads: about five views tall. */
const TALL_ANSWER = markedParagraphs('T', 40, FILLER)

/** The least growth of the content that shows that the tall answer arrived. */
const TALL_ANSWER_MIN_PX = 1000

/** The gate at which the mock model holds the answer of the current case. */
const ANSWER_GATE = 'chat-scroll-stability-live-answer'

/** The scroll-to-bottom button, which shows while the view is above the bottom. */
function scrollToBottomButton(page: Page) {
  return page.getByRole('button', { name: 'Scroll to bottom' })
}

/** Open the view at the width of the test, and send the turn whose earlier answer the reader reads. */
async function seedEarlierAnswer(page: Page, script: ModelScript): Promise<void> {
  await page.setViewportSize(VIEWPORT)
  await waitForAgentStarted(page)
  await sendLongTurn(page, script, 'Reply with twenty-four paragraphs.', EARLIER_ANSWER, 'E23:')
}

/** Send `prompt`, whose answer the model holds at the gate, and wait until the model holds it. Returns its step. */
async function sendHeldTurn(page: Page, script: ModelScript, prompt: string, answer: string): Promise<number> {
  const step = await script.queue({ text: answer, gate: ANSWER_GATE })
  await sendMessage(page, script.prompt(prompt))
  await script.waitForGate(ANSWER_GATE)
  await waitForThinkingIndicator(page)
  return step
}

/** Release the held answer of `step`, and wait until the turn ends and the chat rests. */
async function releaseHeldTurn(page: Page, script: ModelScript, step: number): Promise<void> {
  await script.releaseGate(ANSWER_GATE)
  await script.waitForSteps(step + 1)
  await waitForAgentIdle(page)
  await waitForScrollRest(page)
}

/** A place of the reader above the bottom, and the action that puts the reader there. */
interface ReaderPlace {
  name: string
  place: (page: Page) => Promise<void>
}

const READER_PLACES: ReaderPlace[] = [
  // Just past the 2px tolerance of follow mode. The answer replaces the thinking indicator below the reader.
  {
    name: 'just above the bottom',
    place: page => wheelChatUp(page, 8),
  },
  {
    name: 'half a view above the bottom',
    place: page => wheelChatUp(page, VIEWPORT.height / 2),
  },
  {
    name: 'in the middle of the earlier answer',
    place: page => wheelChatUp(page, VIEWPORT.height * 1.5),
  },
  {
    name: 'at the top of the history',
    place: async (page) => {
      await chatScrollContainer(page).evaluate((container) => {
        container.scrollTop = 0
      })
      await waitForScrollRest(page)
    },
  },
]

test.describe('chat scroll stability while the agent works', () => {
  for (const { name, place } of READER_PLACES) {
    test(`keeps a reader ${name} in place when a tall answer arrives`, async ({ page, authenticatedWorkspace, modelScript }) => {
      await seedEarlierAnswer(page, modelScript)
      const step = await sendHeldTurn(page, modelScript, 'Reply with forty paragraphs.', TALL_ANSWER)
      await place(page)
      await expect(scrollToBottomButton(page), 'the reader left the bottom').toBeVisible()

      const before = await requireTopRow(page)
      const heightBefore = await readScrollHeight(page)
      const watch = await watchRow(page, before.seq)
      await releaseHeldTurn(page, modelScript, step)

      expect(await readScrollHeight(page) - heightBefore, 'the tall answer arrived below the reader').toBeGreaterThan(TALL_ANSWER_MIN_PX)
      await watch.expectStill('while the tall answer arrives')
      await expectRowHeld(page, before, 'after the tall answer arrives')
      await expect(scrollToBottomButton(page)).toBeVisible()
    })
  }

  test('keeps a reader in place while a turn delivers its messages one after another', async ({ page, authenticatedWorkspace, modelScript }) => {
    await seedEarlierAnswer(page, modelScript)
    // The first message calls a tool that needs no approval, so the turn continues with a second message. Claude Code
    // shows no row for its to-do tool, so the reader sees two answers arrive with a pause between them.
    const firstGate = 'chat-scroll-stability-live-first'
    const secondGate = 'chat-scroll-stability-live-second'
    const first = await modelScript.queue({
      text: markedParagraphs('A', 12, FILLER),
      toolCalls: [
        updateTodosToolCall(AgentProvider.CLAUDE_CODE, 'chat-scroll-stability-todos', [
          { step: 'Write the first part', status: 'completed' },
          { step: 'Write the second part', status: 'in_progress' },
        ]),
      ],
      gate: firstGate,
    })
    const second = await modelScript.queue({
      text: markedParagraphs('B', 20, FILLER),
      gate: secondGate,
    })
    await sendMessage(page, modelScript.prompt('Reply in two parts.'))
    await modelScript.waitForGate(firstGate)
    await waitForThinkingIndicator(page)
    await wheelChatUp(page, VIEWPORT.height * 1.5)

    const before = await requireTopRow(page)
    const heightBefore = await readScrollHeight(page)
    const watch = await watchRow(page, before.seq)
    await modelScript.releaseGate(firstGate)
    await modelScript.waitForSteps(first + 1)
    await modelScript.waitForGate(secondGate)
    await expect.poll(() => readScrollHeight(page), { message: 'the first message arrived below the reader' }).toBeGreaterThan(heightBefore + 300)
    await waitForScrollRest(page)
    const heightBetween = await readScrollHeight(page)
    await modelScript.releaseGate(secondGate)
    await modelScript.waitForSteps(second + 1)
    await waitForAgentIdle(page)
    await waitForScrollRest(page)

    expect(await readScrollHeight(page) - heightBetween, 'the second message arrived below the reader').toBeGreaterThan(500)
    await watch.expectStill('while the messages of the turn arrive')
    await expectRowHeld(page, before, 'after the turn ends')
  })

  test('keeps a follower at the bottom when a tall answer arrives', async ({ page, authenticatedWorkspace, modelScript }) => {
    await seedEarlierAnswer(page, modelScript)
    const step = await sendHeldTurn(page, modelScript, 'Reply with forty paragraphs.', TALL_ANSWER)
    await expectAtBottom(page, 'while the answer is held')

    await releaseHeldTurn(page, modelScript, step)

    await expectAtBottom(page, 'after the tall answer arrives')
    await expect(scrollToBottomButton(page)).toBeHidden()
  })

  test('follows the tail again when the reader returns to the bottom before the answer arrives', async ({ page, authenticatedWorkspace, modelScript }) => {
    await seedEarlierAnswer(page, modelScript)
    const step = await sendHeldTurn(page, modelScript, 'Reply with forty paragraphs.', TALL_ANSWER)
    await wheelChatUp(page, VIEWPORT.height)
    await scrollToBottomButton(page).click()
    await expectAtBottom(page, 'after the scroll-to-bottom button')

    await releaseHeldTurn(page, modelScript, step)

    await expectAtBottom(page, 'after the tall answer arrives')
    await expect(scrollToBottomButton(page)).toBeHidden()
  })

  test('returns to the tail when the reader sends a message from above, and follows its answer', async ({ page, authenticatedWorkspace, modelScript }) => {
    await seedEarlierAnswer(page, modelScript)
    await wheelChatUp(page, VIEWPORT.height * 1.5)
    await expect(scrollToBottomButton(page)).toBeVisible()

    const step = await sendHeldTurn(page, modelScript, 'Reply with forty paragraphs.', TALL_ANSWER)
    await expectAtBottom(page, 'after the send')

    await releaseHeldTurn(page, modelScript, step)

    await expectAtBottom(page, 'after the tall answer arrives')
    await expect(scrollToBottomButton(page)).toBeHidden()
  })
})
