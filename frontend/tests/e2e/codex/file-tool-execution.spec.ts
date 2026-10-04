import type { Locator } from '@playwright/test'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codexTest } from '../codex-fixtures'
import { readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { exerciseFileToolExecution } from '../helpers/nativeToolExecution'
import { bashToolCall, editToolCall, writeToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, fileChangeRow, messageBubbles, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { codexAppliedFileChange, requireCodexPatchResult } from './appliedFileChange'
import { readCodexExecResult } from './execResult'

codexTest.describe('codex tool execution', () => {
  codexTest('file edit triggers file change rendering', async ({ authenticatedCodexWorkspace, page, modelScript, leapmuxServer }) => {
    void authenticatedCodexWorkspace // fixture trigger
    const workingDir = authenticatedCodexWorkspace.workingDir
    if (!workingDir)
      throw new Error('The native file proof requires its private working directory.')
    const path = join(workingDir, 'codex-test-file.txt')
    await modelScript.queue(
      { toolCalls: [writeToolCall(AgentProvider.CODEX, 'write-call', { path, content: 'codex was here' })] },
      { text: `I created ${path} with the content "codex was here".` },
    )
    await sendMessage(page, modelScript.prompt(`Create a file called ${path} with the content "codex was here"`))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)

    // A native Write creates a tool row with its line count and a separate added-line diff.
    // The tool title omits the diff badge, so fileChangeRow cannot identify it.
    // The prompt and scripted answer cannot satisfy these tool and diff locators.
    const title = page.locator('[data-tool-message]:visible').filter({ hasText: 'codex-test-file.txt (1 line)' })
    const tool = messageBubbles(page).filter({ has: title }).first()
    await expect(tool).toBeVisible()
    await expect(tool.getByText('codex-test-file.txt', { exact: true })).toBeVisible()
    await expect(tool.getByText('(1 line)', { exact: true })).toBeVisible()
    await expect(tool).toHaveAttribute('data-tool-status', 'completed')
    await expect(page.locator(`[data-file-diff][data-file-path="${path}"]:visible`).filter({ hasText: 'codex was here' }).first()).toBeVisible()
    // Check the scripted answer separately from the native tool result.
    await expect(assistantBubbles(page).filter({ hasText: /codex-test-file|codex was here/ }).first()).toBeVisible()
    expect(readFileSync(path, 'utf8')).toBe('codex was here\n')
    const request = status.requests.find(record => record.stepIndex === 1)
    if (!request)
      throw new Error('The native patch reached no following model request.')
    requireCodexPatchResult(request, 'write-call')
    const itemId = await tool.getAttribute('data-tool-call-id')
    if (!itemId)
      throw new Error('The visible native file tool contains no item identity.')
    const context = { page, leapmuxServer }
    const agent = await currentNativeAgent(context)
    if (agent.agentSessionId.trim() === '')
      throw new Error('The native Codex file proof requires its root session ID.')
    const snapshot = await readNativeMessageSnapshot(context, agent.id)
    const currentMessages = snapshot.messages.filter(message => message.agentSessionId === snapshot.agentSessionId)
    const change = codexAppliedFileChange(currentMessages, snapshot.agentSessionId, itemId, path)
    expect(change.diff).toBe('codex was here\n')
  })
})

/** Compare statistics badges from actual native apply_patch results for one file and multiple files. */
async function badgePresentation(badge: Locator) {
  await expect(badge).toBeVisible()
  return badge.evaluate((element) => {
    const style = globalThis.getComputedStyle(element)
    const title = element.parentElement
    const previous = element.previousElementSibling
    const box = element.getBoundingClientRect()
    const previousBox = previous?.getBoundingClientRect()
    return {
      fontFamily: style.fontFamily,
      fontSize: style.fontSize,
      lineHeight: style.lineHeight,
      titleGap: title ? globalThis.getComputedStyle(title).gap : null,
      gap: previousBox ? Math.round((box.left - previousBox.right) * 100) / 100 : null,
    }
  })
}

codexTest('file-change statistics keep one presentation for one file and multiple files', async ({ page, authenticatedCodexWorkspace, modelScript }) => {
  void authenticatedCodexWorkspace

  // The files must exist before the patch, so the change is an UPDATE and the
  // diff has both sides to state.
  await modelScript.queue(
    { toolCalls: [bashToolCall(AgentProvider.CODEX, 'seed-files', 'printf "old\n" > single.ts; printf "old\n" > first.ts; printf "old\n" > second.ts; ls')] },
    { text: 'The files exist now.' },
  )
  await sendMessage(page, modelScript.prompt('Create the files.'))
  await modelScript.waitForSteps()
  await waitForAgentIdle(page)

  // Codex reports one fileChange item for each changed file. Compare one row from each turn.
  await modelScript.queue(
    { toolCalls: [editToolCall(AgentProvider.CODEX, 'single-edit', { path: 'single.ts', before: 'old', after: 'new' })] },
    { text: 'I changed single.ts.' },
  )
  await sendMessage(page, modelScript.prompt('Change single.ts.'))
  await modelScript.waitForSteps()
  await waitForAgentIdle(page)

  await modelScript.queue(
    {
      toolCalls: [
        editToolCall(AgentProvider.CODEX, 'first-edit', { path: 'first.ts', before: 'old', after: 'new' }),
        editToolCall(AgentProvider.CODEX, 'second-edit', { path: 'second.ts', before: 'old', after: 'new' }),
      ],
    },
    { text: 'I changed first.ts and second.ts.' },
  )
  await sendMessage(page, modelScript.prompt('Change first.ts and second.ts.'))
  await modelScript.waitForSteps()
  await waitForAgentIdle(page)

  const single = await badgePresentation(fileChangeRow(page, 'single.ts').getByTestId('git-diff-stats').first())
  const multiple = await badgePresentation(fileChangeRow(page, 'first.ts').getByTestId('git-diff-stats').first())

  expect(single).toEqual(multiple)
  expect(single.titleGap).not.toBe('normal')
  expect(single.gap).toBeGreaterThan(0)
})

codexTest('reads edits and creates private files with native result and reload proof', async ({ authenticatedCodexWorkspace, page, leapmuxServer, modelScript }) => {
  await exerciseFileToolExecution({ page, modelScript, leapmuxServer, provider: AgentProvider.CODEX, workspaceId: authenticatedCodexWorkspace.workspaceId, readToolResult: readCodexExecResult })
})
