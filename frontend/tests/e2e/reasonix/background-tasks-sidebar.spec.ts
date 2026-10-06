import { reasonixTest } from '../reasonix-fixtures'
import { exerciseReasonixSpawnTranscript } from './childScenario'

reasonixTest('background-tasks-sidebar: subagent spawn creates a prompt and report transcript', async ({ native }) => {
  await exerciseReasonixSpawnTranscript(native)
})
