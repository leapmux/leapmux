import { gooseTest } from '../goose-fixtures'
import { expectUnsupportedFork } from '../helpers/nativeFork'

gooseTest('reports supportsFork false and refuses native session forking', async ({ native }) => {
  await expectUnsupportedFork(native)
})
