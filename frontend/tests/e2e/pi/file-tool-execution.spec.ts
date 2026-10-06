import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { exerciseFileToolExecution, runNativeToolTurn } from '../helpers/nativeToolExecution'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { writeToolCall } from '../helpers/providerToolCalls'
import { toolRows } from '../helpers/ui'
import { piTest } from '../pi-fixtures'

piTest('write tool creates a file and the chat surfaces the path', async ({ authenticatedPiWorkspace, native }) => {
  const workingDir = authenticatedPiWorkspace.workingDir
  if (!workingDir)
    throw new Error('The Pi file proof requires a private working directory.')
  const path = join(workingDir, 'pi-test-file.txt')
  expect(existsSync(path)).toBe(false)
  const { resultRequest } = await runNativeToolTurn(native, {
    toolCalls: [writeToolCall(native.provider, 'write-call', { path, content: 'pi was here' })],
    prompt: 'Use the native write tool to create the supplied file.',
    answer: 'The native write completed.',
  })
  expect(readFileSync(path, 'utf8')).toBe('pi was here')
  expect(nativeToolResult(resultRequest, 'write-call')).toContain('pi-test-file.txt')
  await expect(toolRows(native.page).filter({ hasText: 'pi-test-file.txt' }).first()).toBeVisible()
})

piTest('reads and changes actual scratch bytes through native file tools', async ({ native }) => {
  await exerciseFileToolExecution(native)
})
