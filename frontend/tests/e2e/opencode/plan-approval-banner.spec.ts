import { opencodeTest } from '../opencode-fixtures'
import { exerciseOpenCodeFamilyReadOnlyPlan } from './readOnlyPlan'
import { OPENCODE_PLAN_REMINDER } from './settingsScenario'

opencodeTest('completes a native read-only plan without a dedicated approval banner', async ({ native }) => {
  await exerciseOpenCodeFamilyReadOnlyPlan(native, { defaultPrimaryAgent: 'build', planReminder: OPENCODE_PLAN_REMINDER })
})
