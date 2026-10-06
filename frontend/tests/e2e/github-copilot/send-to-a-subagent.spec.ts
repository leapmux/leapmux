import { copilotTest } from '../copilot-fixtures'
import { openProfiledNativeChild } from '../helpers/runningChildProof'
import { expectUnsupportedSubagent } from '../helpers/unsupportedSubagent'
import { COPILOT_CHILD } from './childScenario'

copilotTest('refuses native child send while the actual child task runs', async ({ native }) => {
  await expectUnsupportedSubagent(native, { operation: 'send', openChild: () => openProfiledNativeChild(native, COPILOT_CHILD) })
})
