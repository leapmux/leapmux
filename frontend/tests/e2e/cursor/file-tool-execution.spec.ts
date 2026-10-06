import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { cursorTest } from '../cursor-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { nativeModelContextText } from '../helpers/nativeScenario'
import { expectFileDiff } from '../helpers/nativeToolExecution'
import { editToolCall, readToolCall, writeToolCall } from '../helpers/providerToolCalls'
import { uniqueMarker } from '../helpers/shellArguments'
import { runCursorNativeOperations } from './nativeExecutionScenario'

cursorTest('reads and changes actual native file bytes with spaces and shell metacharacters', async ({ authenticatedCursorWorkspace, native }) => {
  const directory = authenticatedCursorWorkspace.workingDir
  if (!directory)
    throw new Error('The native Cursor file proof requires a working directory.')
  const suffix = uniqueMarker()
  const file = join(directory, 'native $(printf WRONG_PATH) source file.txt')
  const created = join(directory, 'new native file with spaces.txt')
  const before = `CURSORBEFORE${suffix}`
  const after = `CURSORAFTER${suffix}`
  const written = `CURSORCREATED${suffix}\n`
  writeFileSync(file, `${before}\n`)
  expect(existsSync(created)).toBe(false)
  await runCursorNativeOperations(native, [
    readToolCall(native.provider, 'native-read-before', file),
    editToolCall(native.provider, 'native-edit', { path: file, before, after }),
    readToolCall(native.provider, 'native-read-after', file),
    writeToolCall(native.provider, 'native-create', { path: created, content: written }),
  ], after)
  expect(readFileSync(file, 'utf8')).toBe(`${after}\n`)
  expect(readFileSync(created, 'utf8')).toBe(written)
  await expectFileDiff(native.page, { before, after })
  const next = await sendNativeAnswer(native, 'Use the existing actual file context and reply once.', 'The native file context probe completed.')
  expect(nativeModelContextText(next)).toContain(before)
  expect(nativeModelContextText(next)).toContain(after)
  expect(nativeModelContextText(next)).toContain(written.trim())
  await native.page.reload()
  await expectFileDiff(native.page, { before, after })
})
