import { codexTest } from '../codex-fixtures'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { codexExecContext } from './scenarios'

codexTest.describe('codex tool execution', () => {
  codexTest('command execution shows command, output, and exit code', async ({ native }) => {
    // The commands really run, so the exit code that the card shows is the shell's own. Each command computes its
    // output, so only the command's own output can put it in a tool row. The shared scenario proves the command, the
    // output and the exit code in the rows, and the output and the code in the next model request. Codex runs each
    // command in its exec cell, whose result opens with `Script completed` and `Wall time`; the row draws neither.
    await exerciseShellToolExecution(codexExecContext(native), { absentRowText: ['Script completed', 'Wall time'] })
  })
})
