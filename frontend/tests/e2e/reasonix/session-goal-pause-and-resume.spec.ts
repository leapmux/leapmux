import { reasonixTest } from '../reasonix-fixtures'
import { exerciseReasonixGoalErrorResume, exerciseReasonixGoalPauseResume } from './goalScenario'

reasonixTest('pauses and resumes the retained native goal through its advertised requests after reload', async ({ native }) => {
  await exerciseReasonixGoalPauseResume(native)
})

reasonixTest('resumes the retained native goal through the Worker API after a model error', async ({ native }) => {
  await exerciseReasonixGoalErrorResume(native)
})
