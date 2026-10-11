import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { kiroTest } from '../kiro-fixtures'

kiroTest('proves calculated native stdout and failed stderr reach the next turn', async ({ native }) => {
  // Kiro frames each result as `Output:`, the output, then `Exit Code: N`. The row header states a nonzero code, and
  // the body draws only the output. This test holds the exit-code proof of Kiro; the file test does not.
  await exerciseShellToolExecution(native, { absentRowText: ['Output:', 'Exit Code:'] })
})
