import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { randomUUID } from 'node:crypto'
import { existsSync, mkdtempSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { createNativeToolDirectory } from '../helpers/nativeToolDirectory'
import { waitForNativeToolSteps } from '../helpers/nativeToolExecution'
import { bashToolCall } from '../helpers/providerToolCalls'
import { printfMarkerCommand, quotePosixShellArgument } from '../helpers/shellArguments'
import { assistantBubbles, sendMessage } from '../helpers/ui'
import { readGeminiToolOutput } from './toolResult'

const INJECTION_REFUSAL = 'Command injection detected: command substitution syntax ($(), backticks, <() or >()) found in command arguments. On PowerShell, @() array subexpressions and $() subexpressions are also blocked. This is a security risk and the command was blocked.'

/**
 * Preserve the native refusal and verify printed stdout and a nonzero shell exit.
 *
 * Gemini CLI refuses a command when `$(`, a backtick, `<(` or `>(` stands outside
 * single quotes (`detectBashSubstitution` in its shell tool). The refused case
 * names the hostile directory in DOUBLE quotes, where a POSIX shell would run
 * its `$(touch command-expanded-marker)`. The other cases name their files in
 * single quotes and print their markers without `$(`, so Gemini runs them.
 */
export async function exerciseGeminiShellToolExecution(context: ManagedNativeScenarioContext): Promise<void> {
  const agent = await currentNativeAgent(context)
  const hostileFile = join(createNativeToolDirectory(agent.workingDir), 'native shell output.txt')
  const safeFile = join(mkdtempSync(join(agent.workingDir, 'native shell safe-')), 'native shell output.txt')
  const marker = randomUUID().replaceAll('-', '')
  const cases = [
    { name: 'double-quoted path refusal', command: `${printfMarkerCommand(`SHELL${marker}`, 42)} > "${hostileFile}"; cat "${hostileFile}"`, output: INJECTION_REFUSAL, status: 'completed', rejected: true },
    { name: 'printed stdout', command: `${printfMarkerCommand(`SHELL${marker}`, 42)} > ${quotePosixShellArgument(safeFile)}; cat ${quotePosixShellArgument(safeFile)}`, output: `SHELL${marker}42`, status: 'completed', rejected: false },
    { name: 'printed stderr', command: `${printfMarkerCommand(`SHELLERR${marker}`, 77)} >&2; exit 7`, output: `SHELLERR${marker}77`, status: 'failed', rejected: false },
  ] as const
  expect(existsSync(hostileFile)).toBe(false)
  expect(existsSync(safeFile)).toBe(false)
  for (const [index, scenario] of cases.entries()) {
    const start = (await context.modelScript.status()).stepCount
    const callId = `gemini-shell-${marker}-${index}`
    const answer = `The native ${scenario.name} scenario ended.`
    await context.modelScript.queue({ toolCalls: [bashToolCall(context.provider, callId, scenario.command)] }, { text: answer })
    await sendMessage(context.page, context.modelScript.prompt(`Run the native ${scenario.name} scenario.`))
    await waitForNativeToolSteps(context, start + 2)
    const request = (await context.modelScript.status()).requests.find(row => row.stepIndex === start + 1)
    const output = readGeminiToolOutput(request, callId)
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
    const result = context.page.locator(`[data-testid="message-bubble"][data-tool-call-id="run_shell_command__${callId}"][data-tool-row-role="result"]:visible`)
    const proveResult = async () => {
      await expect(result).toHaveCount(1)
      await expect(result).toHaveAttribute('data-tool-status', scenario.status)
      await expect(result).toContainText(scenario.rejected ? 'Blocked: command substitution detected in shell command.' : scenario.output)
      if (scenario.status === 'failed')
        await expect(result).toContainText(/Error|failed|exit(?:ed)?(?: with)?(?: code)?\s*7/i)
    }
    await proveResult()
    await expect(assistantBubbles(context.page).filter({ hasText: answer }).first()).toBeVisible()
    await context.page.reload()
    await proveResult()
  }
}
