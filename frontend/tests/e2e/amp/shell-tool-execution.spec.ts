import { expect } from '@playwright/test'
import { ampTest } from '../amp-fixtures'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { applyPermissionPreset, chatText } from '../helpers/ui'

/**
 * The installed agent executes the scripted shell command. Calculated output proves that the executor ran the command.
 *
 * The Worker drives Amp's stream JSON protocol. The isolated mock implements Amp's remote service.
 */
ampTest.describe('Amp tool execution', () => {
  ampTest('draws the output of a command', async ({ native }) => {
    // The shared scenario proves the computed output on the page and in the next model request.
    await exerciseShellToolExecution(native, {
      includeFailure: false,
      // Amp states the result as a JSON record. The row draws its output, not the record.
      rowProof: async ({ page }) => {
        expect(await chatText(page)).not.toContain('"exitCode"')
      },
    })
  })
})

ampTest('preserves a literal private shell path with spaces and metacharacters', async ({ native }) => {
  await exerciseShellToolExecution(native, { includeFailure: false, prepare: () => applyPermissionPreset(native.page, 'bypass') })
})
