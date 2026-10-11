import { grokTest } from '../grok-fixtures'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { readGrokShellResult } from './shellResult'

grokTest('proves calculated native stdout and failed stderr reach the next turn', async ({ native }) => {
  await exerciseShellToolExecution({ ...native, readToolResult: readGrokShellResult })
})
