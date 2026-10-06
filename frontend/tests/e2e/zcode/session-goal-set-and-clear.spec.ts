import { pauseResumeClearGoal, scriptedObjective, setGoal } from '../helpers/goalsAndTodos'
import { zcodeTest } from '../zcode-fixtures'

zcodeTest('sets, pauses, resumes, and clears the native goal', async ({ authenticatedZCodeWorkspace, page, modelScript }) => {
  void authenticatedZCodeWorkspace
  await modelScript.fallback({ text: 'Goal turn complete.' })
  const objective = scriptedObjective(modelScript, 'Keep the ZCode session goal until the browser clears it.')
  await setGoal(page, objective)
  await pauseResumeClearGoal(page, objective)
})
