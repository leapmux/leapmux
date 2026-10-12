import { geminiTest } from '../gemini-fixtures'
import { exerciseFileToolExecution } from '../helpers/nativeToolExecution'

geminiTest('reads and changes actual native files and shows the applied diff', async ({ native }) => {
  await exerciseFileToolExecution(native)
})
