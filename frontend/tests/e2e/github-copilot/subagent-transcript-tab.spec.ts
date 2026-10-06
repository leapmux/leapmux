import { copilotTest } from '../copilot-fixtures'
import { exerciseCopilotChildTranscript } from './childScenario'

copilotTest('routes the prompt, response, and completion into the child tab', async ({ native }) => {
  await exerciseCopilotChildTranscript(native)
})
