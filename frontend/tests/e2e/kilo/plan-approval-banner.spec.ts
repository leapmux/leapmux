import { kiloTest } from '../kilo-fixtures'
import { exerciseOpenCodeFamilyReadOnlyPlan } from '../opencode/readOnlyPlan'
import { KILO_PLAN_REMINDER } from './scenarios'

/**
 * Kilo's default primary agent. Kilo renames OpenCode's `build` agent to `code`
 * and deletes `build` (`patchAgents` in Kilo's
 * `packages/opencode/src/kilocode/agent/index.ts`), so the catalog offers no
 * `build`. The Worker's fallback states the same name (`kilo.PrimaryAgentCode`).
 */
const KILO_DEFAULT_PRIMARY_AGENT = 'code'

kiloTest('completes a native read-only plan without a dedicated approval banner', async ({ native }) => {
  await exerciseOpenCodeFamilyReadOnlyPlan(native, { defaultPrimaryAgent: KILO_DEFAULT_PRIMARY_AGENT, planReminder: KILO_PLAN_REMINDER })
})
