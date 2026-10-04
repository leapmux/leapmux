import { existsSync, mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { commandCodeTest, createCommandCodeWorkingDir, expect, openCommandCodeAgent } from '../command-code-fixtures'
import { expectNoNativeStartupControl } from '../helpers/nativeControlObservation'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { tabById } from '../helpers/ui'
import { nativeContext } from './scenarios'

commandCodeTest('keeps the actual untrusted project mod unloaded without a native trust dialog', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const workingDir = createCommandCodeWorkingDir()
  const marker = join(workingDir, 'native-project-mod-executed')
  const mods = join(workingDir, '.commandcode/mods')
  mkdirSync(mods, { recursive: true })
  writeFileSync(join(mods, 'native-project-trust.mjs'), `import {writeFileSync} from 'node:fs';export default function(){writeFileSync(${JSON.stringify(marker)},'Native project mod executed.')}`)
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  await expectNoNativeStartupControl(context, {
    testId: 'control-banner',
    additionalTestIds: ['dialog-editor'],
    start: async () => {
      const agent = await openCommandCodeAgent(leapmuxServer, context.workspaceId, {}, workingDir)
      await tabById(page, agent.agentId).click()
    },
    relatedControl: async () => {
      await sendNativeAnswer(context, 'Return a native answer from the untrusted scratch project.', 'The native project probe completed.')
      expect(existsSync(marker)).toBe(false)
    },
  })
})
