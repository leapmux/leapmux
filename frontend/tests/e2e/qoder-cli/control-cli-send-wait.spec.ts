import { expect } from '@playwright/test'
import { AgentInputState } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { withCleanup } from '../helpers/cleanup'
import { mintCLITokenForAdmin, runCLI } from '../helpers/cli'
import { exerciseExactCLIReply } from '../helpers/nativeExactCLIReply'
import { readNativeInputQueue } from '../helpers/nativeInputQueueIdle'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { queuedInputRow } from '../helpers/steer'
import { userBubbles } from '../helpers/ui'
import { qoderTest } from '../qoder-fixtures'

qoderTest('retries the exact reply while a later native question is open', async ({ native }) => {
  await exerciseExactCLIReply(native)
})

qoderTest('returns the reply to a steer after the original turn ends', async ({ native, page, modelScript, leapmuxServer }) => {
  const { id } = await currentNativeAgent(native)
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const gate = 'qoder-cli-hold-root'
  const rootReply = 'The original release check passed.'
  const steerText = 'Check the second release.'
  const steerReply = 'The second release check passed.'
  await modelScript.queue({
    text: rootReply,
    gate,
  }, { text: steerReply })
  const root = [
    'agent',
    'send',
    id,
    modelScript.prompt('Check the original release.'),
    '--request-id',
    'qoder-root',
  ]
  const steer = [
    'agent',
    'send',
    id,
    modelScript.prompt(steerText),
    '--request-id',
    'qoder-steer',
  ]
  const queued = await withCleanup(async () => {
    await runCLI(cli, root)
    await modelScript.waitForGate(gate)
    const submitted = await runCLI(cli, steer) as { input_id: string }
    await queuedInputRow(page, steerText).getByRole('button', { name: 'Steer', exact: true }).click()
    await retryUntilPass(async () => {
      const queue = await readNativeInputQueue(leapmuxServer, id)
      expect(queue.items.find(item => item.id === submitted.input_id)?.state).toBe(AgentInputState.AWAITING_ECHO)
    })
    await expect(userBubbles(page).filter({ hasText: steerText })).toHaveCount(0)
    return submitted
  }, async () => { await modelScript.releaseGateIfHeld(gate) })
  expect(await runCLI(cli, [
    ...root,
    '--wait',
    '--timeout',
    '25s',
  ])).toMatchObject({
    turn_outcome: 'completed',
    replies: [rootReply],
  })
  expect(await runCLI(cli, [
    ...steer,
    '--wait',
    '--timeout',
    '25s',
  ])).toMatchObject({
    input_id: queued.input_id,
    turn_outcome: 'completed',
    replies: [steerReply],
  })
  await expect(userBubbles(page).filter({ hasText: steerText })).toHaveCount(1)
})
