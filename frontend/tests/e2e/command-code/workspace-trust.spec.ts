import { existsSync, mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { commandCodeTest } from '../command-code-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { exerciseNativeWorkspaceTrustLimit } from '../helpers/nativeWorkspaceTrustLimit'
import { ensureGitRepositoryRoot } from '../helpers/worktree'
import { nativeContext } from './scenarios'

commandCodeTest('keeps the actual untrusted project mod unloaded without a native trust dialog', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  await exerciseNativeWorkspaceTrustLimit(context, {
    projectConfiguration: {
      prepare: ({ directory }) => {
        ensureGitRepositoryRoot(directory)
        const executed = join(directory, 'native-project-mod-executed')
        const mods = join(directory, '.commandcode', 'mods')
        mkdirSync(mods, { recursive: true })
        writeFileSync(join(mods, 'native-project-trust.mjs'), `import {writeFileSync} from 'node:fs';export default function(){writeFileSync(${JSON.stringify(executed)},'Native project mod executed.')}`)
      },
      prove: async (privateContext, { directory }) => {
        await sendNativeAnswer(privateContext, 'Return a native answer from the untrusted scratch project.', 'The native project probe completed.')
        expect(existsSync(join(directory, 'native-project-mod-executed'))).toBe(false)
      },
    },
  })
})
