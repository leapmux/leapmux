import { randomUUID } from 'node:crypto'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { createNativePermissionFileWrite, exerciseNativePermissionWrite } from '../helpers/nativePermission'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { exerciseSteerBeforeTool } from '../helpers/nativeToolSteering'
import { kiloTest } from '../kilo-fixtures'
import { exerciseOpenCodeFamilyDenial } from '../opencode/permissionDenial'

kiloTest('permission-prompts: places queued guidance in the next native model request', async ({ native }) => {
  await exerciseSteerBeforeTool(native, { approveTool: true, resultDividers: 2 })
})

kiloTest('keeps actual file bytes unchanged until the native Allow decision', async ({ native }) => {
  await exerciseNativePermissionWrite(native)
})

kiloTest('keeps exact file bytes after a native Deny decision', async ({ native }) => {
  const agent = await currentNativeAgent(native)
  const fileName = 'native-denied-write.txt'
  const file = join(agent.workingDir, fileName)
  const initialContent = `KEEP_THE_NATIVE_FILE_${randomUUID()}\n`
  // Kilo asks its bash permission for this command, because its default bash rule is ask and no allow rule covers node.
  const operation = await createNativePermissionFileWrite(native, { fileName, callId: 'kilo-denied-write', outputPrefix: 'UNAPPROVED_WRITE', initialContent })
  await exerciseOpenCodeFamilyDenial(native, {
    toolCall: operation.toolCall,
    prompt: 'Run the scripted permission probe.',
    expectUnchanged: () => expect(readFileSync(file, 'utf8')).toBe(initialContent),
  })
})
