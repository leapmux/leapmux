import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { grokTest } from '../grok-fixtures'
import { exerciseAllowThenFeedbackRejection, exerciseRememberedAllow } from '../helpers/nativePermission'
import { bashToolCall } from '../helpers/providerToolCalls'
import { expectSettingsOptionChosen, openWorkspace, savedControlAnswer } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { GROK_AGENT, nativeContext } from './scenarios'

grokTest.describe('Grok Build control requests', () => {
  // Grok's `ask` mode asks before a shell command writes a file. It permits `touch` and `mkdir`, so the commands of
  // `exerciseAllowThenFeedbackRejection` redirect their output.
  // An empty rejection ends the turn. A rejection with a reason puts that reason in native `followup_message`.
  // The same turn then continues.
  grokTest('approves one command and rejects the next with a reason the turn reads', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, GROK_AGENT)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expectSettingsOptionChosen(page, 'approvalMode-ask')
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    await exerciseAllowThenFeedbackRejection(context, { workingDir })
  })

  // Grok's request offers "Yes, and don't ask again for bash commands", and Grok keeps it, so the same command later
  // runs with no request.
  grokTest('an always answer covers the same command in the next turn', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, GROK_AGENT)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expectSettingsOptionChosen(page, 'approvalMode-ask')
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    const file = join(workingDir, 'grok-always.txt')
    // Each run appends the marker, so the file states how many runs happened. The redirect makes `ask` mode ask.
    const command = `printf grok-always >> ${file}`
    await exerciseRememberedAllow(context, {
      scope: 'Always',
      firstCall: bashToolCall(context.provider, 'grok-always-first', command),
      secondCall: bashToolCall(context.provider, 'grok-always-second', command),
      beforeDecision: () => expect(existsSync(file)).toBe(false),
      firstProof: () => expect(readFileSync(file, 'utf8')).toBe('grok-always'),
      secondProof: () => expect(readFileSync(file, 'utf8')).toBe('grok-alwaysgrok-always'),
      viewProof: () => expect(savedControlAnswer(page)).toHaveText('Yes, and don\'t ask again for bash commands'),
    })
  })
})
