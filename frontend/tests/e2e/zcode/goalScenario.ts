import type { NativeScenarioContext } from '../helpers/nativeScenario'
import { pauseResumeClearGoal, scriptedObjective, setGoal } from '../helpers/goalsAndTodos'

/** Set, pause, resume, and clear a native ZCode session goal. The objective and the paused status survive a reload. */
export async function exerciseZCodeGoalCycle(context: NativeScenarioContext): Promise<void> {
  const { page, modelScript } = context
  // ZCode runs native goal turns with the objective as the prompt, and this scenario does not count them.
  // The fallback answers each one.
  await modelScript.fallback({ text: 'Goal turn complete.' })
  const objective = scriptedObjective(modelScript, 'Keep the ZCode session goal until the browser clears it.')
  await setGoal(page, objective)
  await pauseResumeClearGoal(page, objective)
}
