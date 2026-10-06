import { geminiTest } from '../gemini-fixtures'
import { exerciseQueuedTurnWithoutSteering } from '../helpers/nativeToolSteering'

// Gemini CLI offers no steering route: its ACP initialize response states no
// steer capability, and a second session/prompt aborts the running one
// (`Session.prompt` in packages/cli/src/acp/acpSession.ts). LeapMux therefore
// offers Preempt rather than Steer, and the queued message waits for the turn to
// end.
geminiTest('keeps a queued message in the input queue and offers no steer until the native turn ends', async ({ native }) => {
  await exerciseQueuedTurnWithoutSteering(native)
})
