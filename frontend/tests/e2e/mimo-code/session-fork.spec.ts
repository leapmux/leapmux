import { expectUnsupportedFork } from '../helpers/nativeFork'
import { mimoTest } from '../mimo-fixtures'

mimoTest('reports supportsFork false and refuses native session forking', async ({ native }) => {
  await expectUnsupportedFork(native)
})
