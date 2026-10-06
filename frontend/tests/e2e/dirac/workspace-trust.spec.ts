import { chmodSync, existsSync, mkdirSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import process from 'node:process'
import { expect } from '@playwright/test'
import { diracTest } from '../dirac-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { exerciseNativeWorkspaceTrustLimit } from '../helpers/nativeWorkspaceTrustLimit'
import { ensureGitRepositoryRoot } from '../helpers/worktree'
import { nativeContext } from './scenarios'

diracTest('starts with a real project configuration and no native workspace trust barrier', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  await exerciseNativeWorkspaceTrustLimit(context, {
    projectConfiguration: {
      prepare: ({ directory }) => {
        ensureGitRepositoryRoot(directory)
        // The hook writes this file before it reads stdin, so a hook that ran leaves the file even when it received no JSON.
        const executed = join(directory, 'workspace-configuration-marker')
        const program = join(directory, 'workspace-hook.cjs')
        writeFileSync(program, `require('node:fs').writeFileSync(${JSON.stringify(executed)},'WORKSPACE_CONFIG_EXECUTED');process.stdout.write(JSON.stringify({continue:true}))`)
        const hook = join(directory, '.diracrules', 'hooks', 'TaskStart')
        mkdirSync(dirname(hook), { recursive: true })
        writeFileSync(hook, `#!/bin/sh\nexec ${JSON.stringify(process.execPath)} ${JSON.stringify(program)}\n`)
        chmodSync(hook, 0o700)
      },
      prove: async (privateContext, { directory }) => {
        await sendNativeAnswer(privateContext, 'Return one native response from this scratch project.', 'The native project configuration probe completed.')
        expect(existsSync(join(directory, 'workspace-configuration-marker'))).toBe(false)
      },
    },
  })
})
