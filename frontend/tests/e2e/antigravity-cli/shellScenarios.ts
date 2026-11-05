import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { existsSync, mkdtempSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { expectShellToolRows, runNativeToolTurn } from '../helpers/nativeToolExecution'
import { bashToolCall } from '../helpers/providerToolCalls'
import { printfMarkerCommand, quotePosixShellArgument, uniqueMarker } from '../helpers/shellArguments'
import { assistantBubbles, toolCallRow } from '../helpers/ui'
import { readAntigravityToolOutput } from './toolResult'

const ANTIGRAVITY_SHELL_RECORD = [
  'Created At:',
  'Completed At:',
  'Command exited with code',
  'Command output:',
] as const

/** Run real shell commands and verify their native output, exit status, and stored rows. */
export async function exerciseAntigravityShellToolExecution(context: ManagedNativeScenarioContext): Promise<void> {
  const agent = await currentNativeAgent(context)
  const outputFile = join(mkdtempSync(join(agent.workingDir, 'native shell safe-')), 'native shell output.txt')
  const marker = uniqueMarker()
  const cases = [
    {
      name: 'printed stdout',
      command: `${printfMarkerCommand(`SHELL${marker}`, 42)} > ${quotePosixShellArgument(outputFile)}; cat ${quotePosixShellArgument(outputFile)}`,
      output: `SHELL${marker}42`,
      status: 'completed',
      row: { output: `SHELL${marker}42`, printedPrefix: `SHELL${marker}`, exitCode: 0 },
    },
    {
      name: 'printed stderr',
      command: `${printfMarkerCommand(`SHELLERR${marker}`, 77)} >&2; exit 7`,
      output: `SHELLERR${marker}77`,
      status: 'failed',
      row: { output: `SHELLERR${marker}77`, printedPrefix: `SHELLERR${marker}`, exitCode: 7 },
    },
  ] as const
  expect(existsSync(outputFile)).toBe(false)
  for (const [index, scenario] of cases.entries()) {
    const callId = `antigravity-shell-${marker}-${index}`
    const answer = `The native ${scenario.name} scenario ended.`
    const { resultRequest } = await runNativeToolTurn(context, {
      toolCalls: [
        bashToolCall(context.provider, callId, scenario.command),
      ],
      prompt: `Run the native ${scenario.name} scenario.`,
      answer,
    })
    const output = readAntigravityToolOutput(resultRequest, callId)
    expect(output).toContain(scenario.output)
    if (scenario.row.exitCode === 0)
      expect(readFileSync(outputFile, 'utf8')).toBe(`${scenario.output}\n`)
    else
      expect(output).toContain(`Command exited with code ${scenario.row.exitCode}.`)
    const result = toolCallRow(context.page, callId)
    const proveResult = async () => {
      await expect(result).toHaveCount(1)
      await expect(result).toHaveAttribute('data-tool-status', scenario.status)
      await expect(result).toContainText(scenario.output)
      await expectShellToolRows(context, [scenario.row], { absentRowText: ANTIGRAVITY_SHELL_RECORD })
    }
    await proveResult()
    await expect(assistantBubbles(context.page).filter({ hasText: answer }).first()).toBeVisible()
    await context.page.reload()
    await proveResult()
  }
}
