import { expect } from '@playwright/test'
import { codexTest } from '../codex-fixtures'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { toolRows } from '../helpers/ui'
import { codexExecContext } from './scenarios'

codexTest.describe('codex tool execution', () => {
  codexTest('command execution shows command, output, and exit code', async ({ native }) => {
    // The commands really run, so the exit code that the card shows is the shell's own. Each command computes its
    // output, so only the command's own output can put it in a tool row.
    await exerciseShellToolExecution(codexExecContext(native), {
      rowProof: async ({ page, output, printedPrefix, failed }) => {
        const rows = toolRows(page)
        // The output holds no `printf`, so a row that holds it and the printed text shows the command itself.
        await expect(rows.filter({ hasText: 'printf' }).filter({ hasText: printedPrefix }).first(), 'a tool row shows the command').toBeVisible()
        await expect(rows.filter({ hasText: output }).first(), 'a tool row shows the output').toBeVisible()
        if (failed)
          await expect(rows.filter({ hasText: 'Error (exit 7)' }).first(), 'a tool row shows the exit code').toBeVisible()
      },
    })
  })
})

codexTest('returns native shell success and failure output to the following model request', async ({ native }) => {
  await exerciseShellToolExecution(codexExecContext(native))
})
