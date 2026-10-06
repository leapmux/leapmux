import { Buffer } from 'node:buffer'
import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { commandCodeTest, expect } from '../command-code-fixtures'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { currentNativeAgent, nativeTextStep } from '../helpers/nativeScenario'
import { createNativeToolDirectory } from '../helpers/nativeToolDirectory'
import { nativeToolResultAt, waitForNativeToolSteps } from '../helpers/nativeToolExecution'
import { editToolCall, readToolCall, writeToolCall } from '../helpers/providerToolCalls'
import { uniqueMarker } from '../helpers/shellArguments'
import { messageContents, sendMessage, toolCallRow } from '../helpers/ui'

commandCodeTest('reads and changes actual native files and preserves the native result snippet', async ({ native }) => {
  const { page, modelScript } = native
  const agent = await currentNativeAgent(native)
  const directory = createNativeToolDirectory(agent.workingDir)
  const marker = uniqueMarker()
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
    const beforeId = `read-before-${scenario.id}`
    const editId = `edit-${scenario.id}`
    const afterId = `read-after-${scenario.id}`
    const writeId = `write-${scenario.id}`
    const stepIndex = await modelScript.queue(
      { toolCalls: [readToolCall(native.provider, beforeId, file)] },
      { toolCalls: [editToolCall(native.provider, editId, { path: file, before: scenario.requestedBefore, after: scenario.requestedAfter })] },
      { toolCalls: [readToolCall(native.provider, afterId, file)] },
      { toolCalls: [writeToolCall(native.provider, writeId, { path: created, content: written })] },
      nativeTextStep(native, `The native ${scenario.id} file operations ended.`),
    )
    await sendMessage(page, modelScript.prompt(`Read and edit the ${scenario.id} file, read its current bytes, then create the second file.`))
    await waitForNativeToolSteps(native, stepIndex + 5)
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
    const editBubble = toolCallRow(page, editId)
    for (const reloaded of [false, true]) {
      if (reloaded)
        await page.reload()
      await expandNativeResultView(editBubble)
      await expect(messageContents(page).filter({ hasText: `Edited ${file} (1 replacement)` }).first()).toContainText(scenario.nativeAfter)
    }
  }
})
