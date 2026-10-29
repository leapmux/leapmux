import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { exerciseLiveChildTranscript } from '../helpers/liveChildTranscript'
import { selectedAgentTabId } from '../helpers/nativeScenario'
import { bashToolCall, spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { expectRowBecomesFinal, requireRegistryRow } from '../helpers/subagentRegistry'
import { answerControl, assistantBubbles, sendMessage, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'
import { qoderTest } from '../qoder-fixtures'
import { qoderChildTurn } from './childScenario'
import { nativeContext } from './scenarios'

qoderTest.describe('Qoder CLI live child transcript', () => {
  qoderTest('shows the child tool result before its final answer', async ({ native, authenticatedQoderWorkspace }) => {
    const child = await exerciseLiveChildTranscript(native, {
      childWhen: qoderChildTurn('Read the assigned file for the live Qoder child'),
      childTask: 'Read the assigned file for the live Qoder child, then report its marker.',
      parentTask: 'Spawn one subagent to read the child note.',
      toolProof: {
        read: { workingDir: authenticatedQoderWorkspace.workingDir },
      },
    })
    await expectRowBecomesFinal(native.page, child.row)
  })

  qoderTest('answers a child permission in the parent tab', async ({ askingQoderWorkspace, page, modelScript, leapmuxServer }) => {
    const native = await nativeContext({
      page,
      modelScript,
      leapmuxServer,
      workspaceId: askingQoderWorkspace.workspaceId,
    })
    const parentId = await selectedAgentTabId(page)
    const task = 'Write the Qoder child permission marker.'
    const path = join(askingQoderWorkspace.workingDir, 'child-permission.txt')
    await modelScript.rule(
      {
        name: 'qoder child permission tool',
        when: qoderChildTurn(task),
        once: true,
        respond: {
          toolCalls: [
            bashToolCall(native.provider, 'child-permission-call', 'printf QODERCHILDPERMISSION > child-permission.txt'),
          ],
        },
      },
      {
        name: 'qoder child permission answer',
        when: qoderChildTurn(task),
        once: true,
        respond: { text: 'The child wrote the marker.' },
      },
    )
    const start = await modelScript.queue(
      {
        toolCalls: [
          spawnSubagentToolCall(native.provider, 'permission-spawn', {
            description: 'Check child tool permission',
            prompt: modelScript.prompt(task),
            background: false,
          }),
        ],
      },
      { text: 'The parent received the child result.' },
    )
    await sendMessage(page, modelScript.prompt('Spawn the scripted permission child.'))
    await modelScript.waitForSteps(start + 1)
    const banner = await waitForControlBanner(page)
    await expect(banner).toContainText('child-permission.txt')
    expect(await selectedAgentTabId(page)).toBe(parentId)
    expect(existsSync(path)).toBe(false)
    await answerControl(page, 'allow')
    await modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(page)
    expect(readFileSync(path, 'utf8')).toBe('QODERCHILDPERMISSION')
    await expectRowBecomesFinal(page, await requireRegistryRow(page))
    await expect(assistantBubbles(page).filter({ hasText: 'The parent received the child result.' })).toHaveCount(1)
  })
})
