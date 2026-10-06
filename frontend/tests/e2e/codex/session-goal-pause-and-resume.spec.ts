import { codexTest } from '../codex-fixtures'
import { pauseResumeClearGoal, scriptedObjective, setGoal } from '../helpers/goalsAndTodos'
import { sendNativeAnswer } from '../helpers/nativeConversation'

codexTest('pauses and resumes an acknowledged native goal and restores its paused state', async ({ native }) => {
  const { page, modelScript } = native
  await sendNativeAnswer(native, 'Start this native goal session.', 'The native goal session is ready.')
  const gate = 'codex-paused-goal-model'
  // Codex starts a turn of its own on a goal set and on a resume, with the objective as the prompt.
  // The fallback holds each such turn at the gate, so the goal stays active until the browser pauses it.
  await modelScript.fallback({ text: 'The held goal turn ended.', gate })
  const objective = scriptedObjective(modelScript, 'Keep this objective until I clear it.')
  try {
    await setGoal(page, objective, async () => {
      await modelScript.waitForGate(gate)
    })
    await pauseResumeClearGoal(page, objective)
  }
  finally {
    await modelScript.releaseGateIfHeld(gate)
  }
})
