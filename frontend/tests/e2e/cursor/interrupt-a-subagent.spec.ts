import { cursorTest } from '../cursor-fixtures'
import { expectUnsupportedSubagent } from '../helpers/unsupportedSubagent'
import { openCursorRunningChild } from './childScenario'

cursorTest('refuses native child interrupt while the actual child task runs', async ({ native }) => {
  await expectUnsupportedSubagent(native, { operation: 'interrupt', openChild: () => openCursorRunningChild(native) })
})
