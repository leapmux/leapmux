import { expect } from '@playwright/test'
import { commandCodeTest } from '../command-code-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { exerciseShellToolExecution, runNativeToolTurn } from '../helpers/nativeToolExecution'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { commandCodeLoadToolsToolCall } from '../helpers/providerToolCalls'
import { commandCodeLoadedToolNames, commandCodeToolCatalog } from './toolCatalog'

commandCodeTest('checks the complete native catalog and its exact executor lookup before a real shell command', async ({ native }) => {
  const request = await sendNativeAnswer(native, 'Return the actual native catalog before the executor lookup.', 'The native catalog completed.')
  const names = commandCodeToolCatalog(request)
  const executors = ['REPL', 'codemode', 'run_code', 'execute_code']
  expect(names).toContain('shell_command')
  for (const name of executors)
    expect(names).not.toContain(name)
  const { resultRequest } = await runNativeToolTurn(native, {
    toolCalls: [commandCodeLoadToolsToolCall('find-native-code', `select:${executors.join(',')}`)],
    prompt: 'Look up the exact native source executor interfaces.',
    answer: 'The native executor lookup completed.',
  })
  // The native lookup is a fuzzy search, so it loads the closest tool of the catalog when no tool has that name.
  const lookup = nativeToolResult(resultRequest, 'find-native-code')
  expect(lookup).toMatch(/^(?:No deferred tool matched|Loaded \d+ tool schema\(s\))/u)
  const loaded = commandCodeLoadedToolNames(lookup)
  for (const name of executors)
    expect(loaded).not.toContain(name)
  await exerciseShellToolExecution(native, { includeFailure: false })
})
