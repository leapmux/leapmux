import { expectUnsupportedFork } from '../helpers/nativeFork'
import { reasonixTest } from '../reasonix-fixtures'

reasonixTest('reports supportsFork false and refuses native session forking', async ({ native }) => {
  await expectUnsupportedFork(native)
})
