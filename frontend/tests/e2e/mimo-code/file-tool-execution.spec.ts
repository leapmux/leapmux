import { expect } from '@playwright/test'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { createNativeToolDirectory } from '../helpers/nativeToolDirectory'
import { exerciseFileEditSequence, PARITY_BEFORE } from '../helpers/nativeToolExecution'
import { messageContents } from '../helpers/ui'
import { mimoTest } from '../mimo-fixtures'

mimoTest.describe('MiMo Code tool execution', () => {
  // MiMo refuses an edit before the session reads the file.
  // The script creates the file. It reads the file before it edits the file.
  mimoTest('a read and an edit render the file body and the applied diff', async ({ native }) => {
    const agent = await currentNativeAgent(native)
    if (!agent.workingDir)
      throw new Error('The active native agent has no working directory.')
    // The Read is the second step of the edit sequence, so the request at `start + 2` holds the Read result.
    const start = await exerciseFileEditSequence(native, { workingDir: createNativeToolDirectory(agent.workingDir), fileName: 'parity.ts' })
    // A successful native Read adds MiMo's numbered format to the model request. The seed command contains no line number.
    // If Read fails, Edit also fails because the session did not read the file.
    const afterRead = await native.modelScript.requestAt(start + 2)
    expect(JSON.stringify(afterRead.body)).toContain(`1: ${PARITY_BEFORE}`)
    await expect(messageContents(native.page).filter({ hasText: 'has not been read' })).toHaveCount(0)
  })
})
