import { exerciseSteerAfterTool, exerciseSteerDuringText, exerciseStopWithPendingSteer } from '../helpers/nativeToolSteering'
import { zcodeTest } from '../zcode-fixtures'
import { bypassToolRequests } from './scenarios'

// ZCode steers through its command inbox: the provider sends a `sendText` with
// the `guide` delivery (`v4/command`), and the runtime injects it into the
// running turn after the next tool result, or continues the turn with it after
// the model's answer. See steering.go in the provider package. The queue holds
// each steer's row until turn.steerDrained proves the model read it
// (SteerRowAtEcho). An interrupt takes a queued steer back first
// (`deleteQueueItem`), so the steer returns to the queue and runs once.

zcodeTest('delivers steering to the actual native tool turn before its single turn end', async ({ native }) => {
  // The held Bash command must run without a permission prompt, so the turn
  // reaches the tool boundary where the runtime injects the steer.
  await bypassToolRequests(native)
  await exerciseSteerAfterTool(native)
})

zcodeTest('continues the streaming answer with the steer', async ({ native }) => {
  await exerciseSteerDuringText(native, { answer: 'continue' })
})

zcodeTest('a steer pending at an interrupt returns to the queue and runs exactly once', async ({ native }) => {
  await exerciseStopWithPendingSteer(native)
})
