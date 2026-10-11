import { ampTest } from '../amp-fixtures'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { applyPermissionPreset } from '../helpers/ui'

/**
 * The installed agent executes the scripted shell command. Calculated output proves that the executor ran the command.
 *
 * The Worker drives Amp's stream JSON protocol. The isolated mock implements Amp's remote service.
 */
ampTest.describe('Amp tool execution', () => {
  ampTest('draws the output of a successful and a failed command', async ({ native }) => {
    // The shared scenario proves the computed output on the page and in the next model request, and the exit code of
    // the failed command in its row header. Amp states each result as a JSON record with `output` and `exitCode`. The
    // row draws the output, not the record.
    await exerciseShellToolExecution(native, { absentRowText: ['"exitCode"', '"output"'] })
  })
})

ampTest('preserves a literal private shell path with spaces and metacharacters', async ({ native }) => {
  await exerciseShellToolExecution(native, { includeFailure: false, prepare: () => applyPermissionPreset(native.page, 'bypass') })
})
