import { Buffer } from 'node:buffer'
import { randomUUID } from 'node:crypto'
import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { commandCodeTest, expect } from '../command-code-fixtures'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { currentNativeAgent, nativeTextStep } from '../helpers/nativeScenario'
import { createNativeToolDirectory } from '../helpers/nativeToolDirectory'
import { nativeToolResultAt, waitForNativeToolSteps } from '../helpers/nativeToolExecution'
import { editToolCall, readToolCall, writeToolCall } from '../helpers/providerToolCalls'
import { messageContents, sendMessage } from '../helpers/ui'
import { nativeContext } from './scenarios'

commandCodeTest('reads and changes actual native files and preserves the native result snippet', async ({ commandCodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: commandCodeWorkspace.workspaceId })
  const agent = await currentNativeAgent(context)
  const directory = createNativeToolDirectory(agent.workingDir)
  const marker = randomUUID().replaceAll('-', '')
  const cases = [
    { id: 'exact', before: `OLD${marker}\n`, after: `NEW${marker}\n`, requestedBefore: `OLD${marker}\n`, requestedAfter: `NEW${marker}\n`, nativeBefore: `OLD${marker}`, nativeAfter: `NEW${marker}` },
    { id: 'fuzzy', before: '\uFEFFconst value = “old”;\r\n', after: '\uFEFFconst value = “new”;\r\n', requestedBefore: 'const value = "old";\n', requestedAfter: 'const value = "new";\n', nativeBefore: 'const value = “old”;', nativeAfter: 'const value = “new”;' },
  ]
  for (const scenario of cases) {
    const file = join(directory, `${scenario.id}-${marker}.txt`)
    const created = join(directory, `created-${scenario.id}-${marker}.txt`)
    const written = `CREATED${scenario.id}${marker}\n`
    writeFileSync(file, scenario.before)
    expect(existsSync(created)).toBe(false)
    const stepIndex = (await modelScript.status()).stepCount
    const beforeId = `read-before-${scenario.id}`
    const editId = `edit-${scenario.id}`
    const afterId = `read-after-${scenario.id}`
    const writeId = `write-${scenario.id}`
    await modelScript.queue(
      { toolCalls: [readToolCall(context.provider, beforeId, file)] },
      { toolCalls: [editToolCall(context.provider, editId, { path: file, before: scenario.requestedBefore, after: scenario.requestedAfter })] },
      { toolCalls: [readToolCall(context.provider, afterId, file)] },
      { toolCalls: [writeToolCall(context.provider, writeId, { path: created, content: written })] },
      nativeTextStep(context, `The native ${scenario.id} file operations ended.`),
    )
    await sendMessage(page, modelScript.prompt(`Read and edit the ${scenario.id} file, read its current bytes, then create the second file.`))
    await waitForNativeToolSteps(context, stepIndex + 5)
    const beforeResult = await nativeToolResultAt(modelScript, stepIndex + 1, beforeId)
    expect(beforeResult).toContain(scenario.nativeBefore)
    expect(beforeResult).not.toContain(scenario.nativeAfter)
    const afterResult = await nativeToolResultAt(modelScript, stepIndex + 3, afterId)
    expect(afterResult).toContain(scenario.nativeAfter)
    expect(afterResult).not.toContain(scenario.nativeBefore)
    const editResult = await nativeToolResultAt(modelScript, stepIndex + 2, editId)
    expect(editResult).toContain(`Edited ${file} (1 replacement)`)
    expect(editResult).toContain(`1\t${scenario.nativeAfter}`)
    if (scenario.id === 'fuzzy')
      expect(editResult).toContain('the replacement keeps the file’s original punctuation style.')
    expect(readFileSync(file)).toEqual(Buffer.from(scenario.after))
    expect(readFileSync(created)).toEqual(Buffer.from(written))
    // The result view shows three rows until it expands, and the snippet follows the header and the blank row.
    const editBubble = page.locator(`[data-testid="message-bubble"][data-tool-call-id="${editId}"][data-tool-row-role="result"]:visible`)
    for (const reloaded of [false, true]) {
      if (reloaded)
        await page.reload()
      await expandNativeResultView(editBubble)
      await expect(messageContents(page).filter({ hasText: `Edited ${file} (1 replacement)` }).first()).toContainText(scenario.nativeAfter)
    }
  }
})
