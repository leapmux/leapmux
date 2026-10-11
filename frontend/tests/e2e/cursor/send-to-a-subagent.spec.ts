import { cursorTest } from '../cursor-fixtures'
import { expectUnsupportedSubagent } from '../helpers/unsupportedSubagent'
import { openCursorRunningChild } from './childScenario'

cursorTest('refuses native child send while the actual child task runs', async ({ native }) => {
  await expectUnsupportedSubagent(native, { operation: 'send', openChild: () => openCursorRunningChild(native) })
})
