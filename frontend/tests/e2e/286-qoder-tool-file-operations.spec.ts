import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { bashToolCall, editToolCall, readToolCall, writeToolCall } from './helpers/providerToolCalls'
import { sendMessage, waitForAgentIdle } from './helpers/ui'
import { expect, QODER_E2E_SKIP_REASON, qoderTest } from './qoder-fixtures'

/**
 * 286 — Qoder CLI file tool execution.
 *
 * One scripted turn seeds a file with Bash, reads it and edits it. The edit
 * draws its diff rows. The working directory is the proof that each call ran:
 * the seeded line exists only when the Bash call ran, and the edited line only
 * when the Edit call ran. A rendered row would show the same text either way.
 */
qoderTest.skip(!!QODER_E2E_SKIP_REASON, QODER_E2E_SKIP_REASON || '')

const PROVIDER = AgentProvider.QODER

qoderTest.describe('Qoder CLI file tool execution', () => {
  qoderTest('seeds, reads and edits a file, and draws the edit diff', async ({ qoderWorkspace, page, modelScript }) => {
    const { workingDir } = qoderWorkspace
    const fileName = 'qoder-file-probe.txt'
    const filePath = join(workingDir, fileName)

    await modelScript.queue(
      { toolCalls: [bashToolCall(PROVIDER, 'seed-file', `printf "const parityBefore = 1\\n" > ${fileName}`)] },
      { toolCalls: [readToolCall(PROVIDER, 'read-file', filePath)] },
      { toolCalls: [editToolCall(PROVIDER, 'edit-file', { path: filePath, before: 'const parityBefore = 1', after: 'const parityAfter = 2' })] },
      { text: 'The file is edited.' },
    )
    await sendMessage(page, modelScript.prompt('Create the file, read it, then change parityBefore to parityAfter.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)

    const diff = page.locator('[data-file-diff]:visible')
    await expect(diff.filter({ hasText: 'const parityAfter = 2' }).first()).toBeVisible()
    await expect(diff.filter({ hasText: 'const parityBefore = 1' }).first()).toBeVisible()
    const onDisk = readFileSync(filePath, 'utf8')
    expect(onDisk, 'the edit landed on disk').toContain('const parityAfter = 2')
    expect(onDisk, 'the edit replaced the seeded line').not.toContain('const parityBefore = 1')
  })

  qoderTest('writes a new file and lands its content on disk', async ({ qoderWorkspace, page, modelScript }) => {
    const { workingDir } = qoderWorkspace
    const fileName = 'qoder-written-probe.txt'
    const filePath = join(workingDir, fileName)

    await modelScript.queue(
      { toolCalls: [writeToolCall(PROVIDER, 'write-file', { path: filePath, content: 'written-42\n' })] },
      { text: 'The file is written.' },
    )
    await sendMessage(page, modelScript.prompt(`Write ${fileName} with one marker line.`))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)

    expect(readFileSync(filePath, 'utf8'), 'the write landed on disk').toContain('written-42')
  })
})
