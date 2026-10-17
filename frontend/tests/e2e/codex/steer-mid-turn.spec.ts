import { expect } from '@playwright/test'
import { INTERRUPTION_MARKER } from '../../../src/components/chat/assembledMessage'
import { codexTest } from '../codex-fixtures'
import { finishCleanup, withCleanup } from '../helpers/cleanup'
import { parseGenerationCounters } from '../helpers/generationProgress'
import { submitGoal } from '../helpers/goalsAndTodos'
import { expectNoUnrenderedRow } from '../helpers/nativeConversation'
import { exerciseSteerAfterTool, NATIVE_TOOL_STEERING_TEXT } from '../helpers/nativeToolSteering'
import { uniqueMarker } from '../helpers/shellArguments'
import { steerQueuedInput } from '../helpers/steer'
import { assistantBubbles, interruptButton, messageContents, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { codexAsyncExecRules } from './asyncExec'

codexTest.describe('steering during a Codex goal turn', () => {
  codexTest('offers Steer for input queued during a goal turn', async ({
    authenticatedCodexWorkspace,
    page,
    modelScript,
  }) => {
    await withCleanup(async () => {
      void authenticatedCodexWorkspace

      // Start the process before the side-band goal command asks Codex to start
      // its own turn. That turn has no queue input to supply its classification.
      const start = await modelScript.queue({ text: 'ready' })
      await sendMessage(page, modelScript.prompt('Reply with the single word: ready'))
      await modelScript.waitForSteps(start + 1)
      await waitForAgentIdle(page)
      // The objective carries the marker because CODEX starts the next turn
      // itself, with the objective as its prompt: an unmarked goal reaches the
      // ambient scenario, which refuses it. The goal turn must still be RUNNING
      // when the steer arrives, so its answer is held open.
      await modelScript.fallback({ text: 'Working on the objective.', gate: 'codex-steer-mid-turn-hold-1' })
      await submitGoal(page, modelScript.prompt('Inspect this repository until I send a steering message. Do not stop before that message.'))

      // The Interrupt button proves that the provider-started goal turn runs.
      // Send while that condition still holds, so the message enters the queue.
      await expect(interruptButton(page)).toBeVisible()
      await steerQueuedInput(page, {
        message: modelScript.prompt('Stop now, mark the goal complete, and reply with STEERED.'),
        match: 'Stop now',
      })
    }, async () => {
      await finishCleanup([modelScript.releaseGateIfHeld('codex-steer-mid-turn-hold-1')])
    })
  })
})

codexTest('delivers steering to the actual native tool turn before its single turn end', async ({ native }) => {
  await native.modelScript.rule(...codexAsyncExecRules(NATIVE_TOOL_STEERING_TEXT))
  await exerciseSteerAfterTool(native, { expectDisplayedOutput: false })
})

// A steer preempts the response that streams (Codex's instant_interrupt
// feature). Codex drops that response: its partial answer never completes, and
// the follow-up request answers the steer. The partial answer stays in the
// transcript with its interruption marker, before the follow-up answer, and not
// after it at the turn end.
codexTest('keeps the answer that a steer preempts, marked and before the follow-up answer', async ({ native }) => {
  const { page, modelScript } = native
  const marker = uniqueMarker()
  const head = `PREEMPTEDHEAD${marker}`
  const tail = `NEVERSTREAMED${marker}`
  const reply = `STEEREDREPLY${marker}`
  const gate = `codex-preempted-answer-${marker}`
  try {
    const start = await modelScript.queue(
      { text: `${head} ${tail} ends the answer.`, stream: { chunkChars: head.length, delayMs: 0, gates: [{ afterChunk: 1, name: gate }] } },
      { text: reply },
    )
    modelScript.allowUnconsumed('The steer preempts the held answer after its first chunk, and Codex drops that response.')
    await sendMessage(page, modelScript.prompt('Stream the scripted answer.'))
    await modelScript.waitForGate(gate)
    // The first child of each count holds its text for a screen reader, such as
    // "8 tokens". A count above zero shows that the Worker received the first chunk.
    const counts = page.locator('[data-testid="thinking-indicator"]:visible [data-animated-count] > :first-child')
    await expect.poll(async () => (await counts.allTextContents()).some(count => (parseGenerationCounters(count).tokens ?? 0) > 0), {
      message: 'the live token counter shows that the Worker received the first chunk',
    }).toBe(true)

    await steerQueuedInput(page, {
      message: modelScript.prompt('Stop that answer and reply with the steered reply.'),
      match: 'Stop that answer',
    })
    await modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(page)

    for (const reload of [false, true]) {
      if (reload)
        await page.reload()
      await expect(assistantBubbles(page).filter({ hasText: head }).filter({ hasText: INTERRUPTION_MARKER }), 'the preempted answer keeps its interruption marker').toHaveCount(1)
      await expect(messageContents(page).filter({ hasText: tail }), 'no row draws the text that never streamed').toHaveCount(0)
      await expect(assistantBubbles(page).filter({ hasText: reply })).toHaveCount(1)
      const texts = await messageContents(page).allTextContents()
      const preempted = texts.findIndex(text => text.includes(head))
      const followUp = texts.findIndex(text => text.includes(reply))
      expect(preempted, 'the transcript draws the preempted answer').toBeGreaterThanOrEqual(0)
      expect(preempted, 'the preempted answer stays before the follow-up answer').toBeLessThan(followUp)
      await expectNoUnrenderedRow(page)
    }
  }
  finally {
    await modelScript.releaseGateIfHeld(gate)
  }
})
