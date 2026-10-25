import { existsSync, mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { exerciseNativeWorkspaceTrustLimit, instructionFileConfiguration } from '../helpers/nativeWorkspaceTrustLimit'
import { newProviderWorkingDir } from '../helpers/providerWorkingDir'
import { answerControl, assistantBubbles, expectNoControlBanner, openWorkspace, waitForControlBanner } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { zcodeTest } from '../zcode-fixtures'
import { nativeContext, ZCODE_AGENT } from './scenarios'

/** The answer of the first turn, which states the hooks that wait for trust. */
const FIRST_TURN_ANSWER = 'The first turn ran without the project hooks.'

/** The answer of the one turn after the decision. */
const TRUST_TURN_ANSWER = 'The hook trust decision reached this turn.'

/** Write a project whose UserPromptSubmit hook appends to a marker file, and return the path of the marker. */
function writeHookProject(workingDir: string): string {
  const marker = join(workingDir, 'hook-marker.txt')
  mkdirSync(join(workingDir, '.zcode'), { recursive: true })
  writeFileSync(join(workingDir, '.zcode', 'config.json'), JSON.stringify({
    hooks: { enabled: true, events: { UserPromptSubmit: [{ hooks: [{ type: 'command', command: `echo ran >> '${marker}'` }] }] } },
  }))
  return marker
}

// ZCode's trust controls a project's hooks, and nothing else: an instruction file loads without a question.
zcodeTest('starts and reads a private project without a workspace trust request', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  await exerciseNativeWorkspaceTrustLimit(context, { projectConfiguration: instructionFileConfiguration('AGENTS.md') })
})

// ZCode runs a project's hooks only after the user trusts them, and it shows its hook review only to a subscriber of
// its desktop protocol. ZCode states the hooks that wait for trust when the first turn starts, and that turn runs
// without them. The Worker asks when the turn ends. Trust answers ZCode's review, and the running session runs the
// hooks from its next prompt. Do not trust leaves them blocked.
for (const decision of ['allow', 'deny'] as const) {
  zcodeTest(`keeps the project's hooks blocked until the hook trust decision is ${decision}`, async ({ page, modelScript, leapmuxServer, authenticatedEmptyWorkspace }) => {
    const { workspaceId } = authenticatedEmptyWorkspace
    const workingDir = newProviderWorkingDir(ZCODE_AGENT, 'zcode-trust-project-')
    const marker = writeHookProject(workingDir)
    await openProviderAgent(leapmuxServer, workspaceId, ZCODE_AGENT, { workingDir })
    await openWorkspace(page, workspaceId)
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId })
    const before = await currentNativeAgent(context)

    await sendNativeAnswer(context, 'Reply once before the hook trust decision.', FIRST_TURN_ANSWER)
    expect(existsSync(marker), 'the untrusted hook did not run at the first prompt').toBe(false)
    const banner = await waitForControlBanner(page)
    await expect(banner).toContainText(`Trust the folder ${workingDir}`)
    await expect(banner).toContainText('ZCode loads the hooks of a project only from a folder that you trust')

    await answerControl(page, decision)
    await expectNoControlBanner(page)
    await sendNativeAnswer(context, 'Reply once after the hook trust decision.', TRUST_TURN_ANSWER)
    if (decision === 'allow')
      expect(existsSync(marker), 'the trusted hook ran at the prompt').toBe(true)
    else
      expect(existsSync(marker), 'the untrusted hook stayed blocked').toBe(false)
    // The decision keeps the agent, its session and its folder: ZCode applies the trust to the running session.
    const after = await currentNativeAgent(context)
    expect(after.id).toBe(before.id)
    expect(after.agentSessionId).toBe(before.agentSessionId)
    expect(after.workingDir).toBe(workingDir)

    // The answered question stays answered.
    await page.reload()
    await expect(assistantBubbles(page).filter({ hasText: TRUST_TURN_ANSWER }).first()).toBeVisible()
    await expectNoControlBanner(page)
  })
}
