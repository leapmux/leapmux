import { droidTest } from '../droid-fixtures'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'

droidTest('runs successful and failed native commands with their actual output', async ({ native }) => {
  await exerciseShellToolExecution(native)
})
