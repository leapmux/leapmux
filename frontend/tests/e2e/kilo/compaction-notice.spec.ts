import { kiloTest } from '../kilo-fixtures'
import { exerciseFailedOpenCodeCompactionNotice, exerciseOpenCodeCompactionNotice } from '../opencode/compactionScenario'

// Kilo's daemon event uses the shared native completed-compaction reader.
kiloTest('stores one native completed compaction event and preserves its divider after reload', async ({ native }) => {
  await exerciseOpenCodeCompactionNotice(native)
})

kiloTest('preserves context after a failed summary and records only the successful native retry', async ({ native }) => {
  await exerciseFailedOpenCodeCompactionNotice(native)
})
