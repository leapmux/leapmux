import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { cursorTest } from '../cursor-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { currentNativeAgent, nativeModelContextText } from '../helpers/nativeScenario'
import { createNativeToolDirectory } from '../helpers/nativeToolDirectory'
import { bashToolCall } from '../helpers/providerToolCalls'
import { quotePosixShellArgument, uniqueMarker } from '../helpers/shellArguments'
import { messageBubbles } from '../helpers/ui'
import { cursorNativeToolOutput, runCursorNativeOperations } from './nativeExecutionScenario'

cursorTest('executes actual native shell output and preserves a nonzero exit status', async ({ native }) => {
  const suffix = uniqueMarker()
  const success = `CURSORSHELL${suffix}42`
  const failure = `CURSORSTDERR${suffix}77`
  const agent = await currentNativeAgent(native)
  if (!agent.workingDir)
    throw new Error('The native Cursor shell requires its private working directory.')
  const outputFile = join(createNativeToolDirectory(agent.workingDir), 'native-shell-output.txt')
  const quotedOutput = quotePosixShellArgument(outputFile)
  await runCursorNativeOperations(native, [
    bashToolCall(native.provider, 'native-shell-success', `printf 'CURSORSHELL${suffix}%s\\n' "$((40 + 2))" > ${quotedOutput} && cat ${quotedOutput}`),
    bashToolCall(native.provider, 'native-shell-failure', `printf 'CURSORSTDERR${suffix}%s\\n' "$((70 + 7))" >&2; exit 7`),
  ], failure)
  expect(await cursorNativeToolOutput(native, 'native-shell-success')).toMatchObject({ exitCode: 0, stdout: `${success}\n`, stderr: '' })
  expect(readFileSync(outputFile, 'utf8')).toBe(`${success}\n`)
  expect(existsSync(join(agent.workingDir, 'command-expanded-marker'))).toBe(false)
  expect(await cursorNativeToolOutput(native, 'native-shell-failure')).toMatchObject({ exitCode: 7, stdout: '', stderr: `${failure}\n` })
  await expect(messageBubbles(native.page).filter({ hasText: failure }).first()).toContainText(/exit\D*7/i)
  const next = await sendNativeAnswer(native, 'Use the actual native shell context and reply once.', 'The shell context probe completed.')
  expect(nativeModelContextText(next)).toContain(success)
  expect(nativeModelContextText(next)).toContain(failure)
})
