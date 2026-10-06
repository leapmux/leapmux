import { exerciseInterruptTurn } from '../helpers/nativeLifecycle'
import { uniqueMarker } from '../helpers/shellArguments'
import { userBubbles } from '../helpers/ui'
import { expect, lettaTest } from '../letta-fixtures'

lettaTest('stops a native model turn and resumes its paused queue', async ({ native }) => {
  // `abort_message` stops Letta's loop at once (`WAITING_ON_INPUT` and an `Interrupted`
  // status), but Letta Code 0.34.2 does not cancel a model request that is in flight. The
  // App Server sends the `cancelled` turn end when the held answer arrives, and it drops
  // that answer.
  await exerciseInterruptTurn(native, { kind: 'model', heldModelTurnEnd: 'after-answer' })
})

lettaTest('stops an actual native tool and accepts the next queued turn', async ({ native }) => {
  await exerciseInterruptTurn(native, { kind: 'tool' })
})

lettaTest('draws the message that the reader sends after an interrupt once', async ({ native }) => {
  const marker = uniqueMarker()
  const prompt = `Continue after the interrupt AFTERSTOP${marker}.`
  // Letta Code 0.34.2 queues the first message after an interrupt, and it echoes a queued
  // message as a `user_message` when the message starts. LeapMux stored the message when the
  // reader sent it, so that echo must draw no second bubble. The helper returns after the
  // answer to this message is visible. The echo precedes that answer, so the count is final.
  await exerciseInterruptTurn(native, {
    kind: 'model',
    heldModelTurnEnd: 'after-answer',
    continuation: { prompt, answer: `AFTERSTOPANSWER${marker}` },
  })
  await expect(userBubbles(native.page).filter({ hasText: prompt })).toHaveCount(1)
})
