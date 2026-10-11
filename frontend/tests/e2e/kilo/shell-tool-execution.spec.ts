import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { kiloTest } from '../kilo-fixtures'
import { readOpenCodeShellOutcome } from '../opencode/nativeShellOutcome'

kiloTest('keeps actual native shell output and a failed command result', async ({ native }) => {
  await exerciseShellToolExecution({ ...native, readToolResult: (request, callId) => readOpenCodeShellOutcome(native, request, callId) })
})
