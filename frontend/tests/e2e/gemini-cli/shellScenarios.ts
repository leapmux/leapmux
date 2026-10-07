import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { existsSync, mkdtempSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { createNativeToolDirectory } from '../helpers/nativeToolDirectory'
import { expectShellToolRows, runNativeToolTurn } from '../helpers/nativeToolExecution'
import { bashToolCall } from '../helpers/providerToolCalls'
import { printfMarkerCommand, quotePosixShellArgument, uniqueMarker } from '../helpers/shellArguments'
import { assistantBubbles, toolCallRow } from '../helpers/ui'
import { readGeminiToolOutput } from './toolResult'

const INJECTION_REFUSAL = 'Command injection detected: command substitution syntax ($(), backticks, <() or >()) found in command arguments. On PowerShell, @() array subexpressions and $() subexpressions are also blocked. This is a security risk and the command was blocked.'

/**
 * The fixed parts of the record that Gemini CLI wraps around the output of a command for its model:
 * `<untrusted_context>\nOutput: …\nExit Code: N\nProcess Group PGID: N\n</untrusted_context>`.
 */
const GEMINI_SHELL_RECORD = ['<untrusted_context>', 'Output:', 'Exit Code:', 'Process Group PGID'] as const

/**
 * Preserve the native refusal and verify printed stdout and a nonzero shell exit.
 *
 * Gemini CLI refuses a command when `$(`, a backtick, `<(` or `>(` stands outside
 * single quotes (`detectBashSubstitution` in its shell tool). The refused case
 * names the hostile directory in DOUBLE quotes, where a POSIX shell would run
 * its `$(touch command-expanded-marker)`. The other cases name their files in
 * single quotes and print their markers without `$(`, so Gemini runs them.
 *
 * The rows of the two commands that ran take the shared shell row proof
 * (`expectShellToolRows`) before and after a reload.
 */
export async function exerciseGeminiShellToolExecution(context: ManagedNativeScenarioContext): Promise<void> {
  const agent = await currentNativeAgent(context)
  const hostileFile = join(createNativeToolDirectory(agent.workingDir), 'native shell output.txt')
  const safeFile = join(mkdtempSync(join(agent.workingDir, 'native shell safe-')), 'native shell output.txt')
  const marker = uniqueMarker()
  const cases = [
    { name: 'double-quoted path refusal', command: `${printfMarkerCommand(`SHELL${marker}`, 42)} > "${hostileFile}"; cat "${hostileFile}"`, output: INJECTION_REFUSAL, status: 'completed', rejected: true, row: undefined },
    { name: 'printed stdout', command: `${printfMarkerCommand(`SHELL${marker}`, 42)} > ${quotePosixShellArgument(safeFile)}; cat ${quotePosixShellArgument(safeFile)}`, output: `SHELL${marker}42`, status: 'completed', rejected: false, row: { output: `SHELL${marker}42`, printedPrefix: `SHELL${marker}`, exitCode: 0 } },
    { name: 'printed stderr', command: `${printfMarkerCommand(`SHELLERR${marker}`, 77)} >&2; exit 7`, output: `SHELLERR${marker}77`, status: 'failed', rejected: false, row: { output: `SHELLERR${marker}77`, printedPrefix: `SHELLERR${marker}`, exitCode: 7 } },
  ] as const
  expect(existsSync(hostileFile)).toBe(false)
  expect(existsSync(safeFile)).toBe(false)
  for (const [index, scenario] of cases.entries()) {
    const callId = `gemini-shell-${marker}-${index}`
    const answer = `The native ${scenario.name} scenario ended.`
    const { resultRequest } = await runNativeToolTurn(context, {
      toolCalls: [bashToolCall(context.provider, callId, scenario.command)],
      prompt: `Run the native ${scenario.name} scenario.`,
      answer,
    })
    const output = readGeminiToolOutput(resultRequest, callId)
    expect(output).toContain(scenario.output)
    if (scenario.rejected) {
      expect(output).toBe(INJECTION_REFUSAL)
      expect(output).not.toContain(`SHELL${marker}42`)
    }
    if (scenario.name === 'printed stdout')
      expect(readFileSync(safeFile, 'utf8')).toBe(`${scenario.output}\n`)
    if (scenario.status === 'failed')
      expect(output).toMatch(/\nExit Code: 7\nProcess Group PGID: [1-9]\d*\n<\/untrusted_context>$/)
    expect(existsSync(hostileFile)).toBe(false)
    expect(existsSync(join(agent.workingDir, 'command-expanded-marker'))).toBe(false)
    const result = toolCallRow(context.page, `run_shell_command__${callId}`)
    const proveResult = async () => {
      await expect(result).toHaveCount(1)
      await expect(result).toHaveAttribute('data-tool-status', scenario.status)
      await expect(result).toContainText(scenario.rejected ? 'Blocked: command substitution detected in shell command.' : scenario.output)
      // The row draws the output of the command, without the record that Gemini wraps around it for its model.
      if (scenario.row)
        await expectShellToolRows(context, [scenario.row], { absentRowText: GEMINI_SHELL_RECORD })
    }
    await proveResult()
    await expect(assistantBubbles(context.page).filter({ hasText: answer }).first()).toBeVisible()
    await context.page.reload()
    await proveResult()
  }
}
