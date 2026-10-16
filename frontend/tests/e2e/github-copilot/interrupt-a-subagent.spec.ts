// The native background-task cancellation route is not integrated.
// See https://github.com/leapmux/leapmux/issues/505.
import { copilotTest } from '../copilot-fixtures'
import { openProfiledNativeChild } from '../helpers/runningChildProof'
import { expectUnsupportedSubagent } from '../helpers/unsupportedSubagent'
import { COPILOT_CHILD } from './childScenario'

copilotTest('refuses native child interrupt while the actual child task runs', async ({ native }) => {
  await expectUnsupportedSubagent(native, { operation: 'interrupt', openChild: () => openProfiledNativeChild(native, COPILOT_CHILD) })
})
