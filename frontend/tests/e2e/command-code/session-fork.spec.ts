import { commandCodeTest } from '../command-code-fixtures'
import { expectUnsupportedFork } from '../helpers/nativeFork'

commandCodeTest('reports supportsFork false and refuses native session forking', async ({ native }) => {
  await expectUnsupportedFork(native)
})
