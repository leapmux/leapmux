import { cursorTest } from '../cursor-fixtures'
import { expectUnsupportedFork } from '../helpers/nativeFork'

cursorTest('reports supportsFork false and refuses native session forking', async ({ native }) => {
  await expectUnsupportedFork(native)
})
