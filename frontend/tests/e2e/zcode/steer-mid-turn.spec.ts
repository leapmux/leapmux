import { exerciseSteerAfterTool, exerciseSteerDuringText, exerciseStopWithPendingSteer } from '../helpers/nativeToolSteering'
import { zcodeTest } from '../zcode-fixtures'
import { bypassToolRequests } from './scenarios'

// ZCode steers through the session mailbox: the provider drops an envelope in
// $ZCODE_MAILBOX_ROOT/<sessionId>/unread, and the runtime's builtin drain hook
// feeds it to steerTurn at the next tool boundary (PostToolUse) or continues
// the turn with it at the model's stop (Stop). The wire protocol offers no
// steering input of its own -- see steering_test.go in the provider package.
// The queue holds each steer's row until turn.steerDrained proves the model
// read it (SteerRowAtEcho), so an interrupted steer returns to the queue.

zcodeTest('delivers steering to the actual native tool turn before its single turn end', async ({ native }) => {
  // The held Bash command must run without a permission prompt, so the turn
  // reaches the tool boundary the mailbox drains at.
  await bypassToolRequests(native)
  await exerciseSteerAfterTool(native)
})

zcodeTest('continues the streaming answer with the steer the Stop hook drains', async ({ native }) => {
  await exerciseSteerDuringText(native, { answer: 'continue' })
})

zcodeTest('a steer pending at an interrupt returns to the queue and runs exactly once', async ({ native }) => {
  await exerciseStopWithPendingSteer(native)
})
