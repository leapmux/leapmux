import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { qwenTest } from '../qwen-fixtures'

qwenTest('proves calculated native stdout and failed stderr reach the next turn', async ({ native }) => {
  await exerciseShellToolExecution(native, { absentRowText: ['Command:', 'Directory:', 'Exit Code:', 'Process Group PGID:'] })
})
