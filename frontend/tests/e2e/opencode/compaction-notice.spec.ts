import { opencodeTest } from '../opencode-fixtures'
import { exerciseFailedOpenCodeCompactionNotice, exerciseOpenCodeCompactionNotice } from './compactionScenario'

// The daemon event supplies the completed boundary that the ACP stream omits.
opencodeTest('stores one native completed compaction event and preserves its divider after reload', async ({ native }) => {
  await exerciseOpenCodeCompactionNotice(native)
})

opencodeTest('preserves context after a failed summary and records only the successful native retry', async ({ native }) => {
  await exerciseFailedOpenCodeCompactionNotice(native)
})
