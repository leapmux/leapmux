import { expect } from '@playwright/test'
import { codebuddyTest } from '../codebuddy-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { currentNativeAgent, expectNativeOptionValue, expectSameNativeSession, nativeModelContextText, nativeOptionValue, nativeTextStep } from '../helpers/nativeScenario'
import { expectSettingsOptionsOffered, waitForNativeOptionApplied } from '../helpers/nativeSettings'
import { readNativeSidebarSnapshot } from '../helpers/nativeSidebarSnapshot'
import { withNativeStartupWorker } from '../helpers/nativeStartupWorker'
import { codebuddyTaskOutputToolCall, codebuddyTeammateToolCall } from '../helpers/providerToolCalls'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { expectRunningChildCompletes, heldChildIdentity, heldChildOptions, nativeChildScriptContext, openRunningNativeChild } from '../helpers/runningChildProof'
import { chooseSettingsOption, openWorkspace, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { nativeContext, nativeLaunch } from './scenarios'

codebuddyTest('passes the native Swarm flag at launch and retains its setting after reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }, testInfo) => {
  const initial = await nativeContext({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: authenticatedEmptyWorkspace.workspaceId,
  })
  await withNativeStartupWorker(initial, nativeLaunch(initial), {}, async (workerId, wrapper) => {
    const server = { ...leapmuxServer, workerId }
    const context = await nativeContext({
      page,
      modelScript,
      leapmuxServer: server,
      workspaceId: initial.workspaceId,
    })
    const opening = openProviderAgent(server, context.workspaceId, context.providerAgent, {
      directoryPrefix: 'codebuddy-swarm-launch-',
      optionValues: { swarmMode: 'true' },
    })
    const entry = await Promise.race([
      wrapper.entry,
      opening.then(() => wrapper.entry),
    ])
    expect(entry.argv.filter(argument => argument === '--swarm')).toHaveLength(1)
    await testInfo.attach('codebuddy-native-swarm-argv', {
      body: JSON.stringify(entry),
      contentType: 'application/json',
    })
    await wrapper.release()
    await opening
    await openWorkspace(page, context.workspaceId)
    await expectNativeOptionValue(context, 'swarmMode', 'true')
    await expectSettingsOptionsOffered(page, 'swarmMode', ['false', 'true'])
    await sendNativeAnswer(context, 'Reply once in the native Swarm session.', 'The native Swarm launch completed its turn.')
    const before = await currentNativeAgent(context)
    await page.reload()
    await expectNativeOptionValue(context, 'swarmMode', 'true')
    await sendNativeAnswer(context, 'Reply after reloading the native Swarm session.', 'The native Swarm setting survived reload.')
    expectSameNativeSession(before, await currentNativeAgent(context), 'Reloading native Swarm mode')
  })
})

codebuddyTest('enables native Swarm from Off and keeps the parent session through a real teammate', async ({ native }, testInfo) => {
  await expectNativeOptionValue(native, 'swarmMode', 'false')
  const empty = await currentNativeAgent(native)
  await chooseSettingsOption(native.page, 'swarmMode-true')
  await waitForNativeOptionApplied(native, 'swarmMode', 'true')
  // An empty native session cannot resume; the restart publishes a new one.
  await retryUntilPass(async () => {
    expect((await currentNativeAgent(native)).agentSessionId).not.toBe(empty.agentSessionId)
  })
  await sendNativeAnswer(native, 'Keep this context before the native Swarm teammate.', 'The native parent is ready for its teammate.')
  const before = await currentNativeAgent(native)
  const settings = {
    model: nativeOptionValue(before, 'model'),
    permissionMode: nativeOptionValue(before, 'permissionMode'),
  }
  const script = nativeChildScriptContext(native)
  const identity = heldChildIdentity(script)
  const spawn = codebuddyTeammateToolCall(identity.spawn.id, { teammateName: 'native-swarm-mate', description: identity.description, prompt: identity.prompt })
  const completedReply = 'The native teammate completion notice was received.'
  const options = heldChildOptions(script, { ...identity, spawn }, {
    rules: [
      {
        name: 'the native teammate completion notice',
        when: {
          lastMessage: { role: 'user', text: '<teammate-message teammate_id="system" summary="native-swarm-mate completed:' },
        },
        respond: { text: completedReply },
        once: true,
      },
    ],
  })
  const child = await openRunningNativeChild(native, options)
  const task = (await readNativeSidebarSnapshot(native, child.parentId)).backgroundTasks.find(task => task.childAgentId === child.childId)
  if (!task)
    throw new Error('The running native teammate has no Worker task identity.')
  await expectRunningChildCompletes({
    ...child,
    finish: async () => {
      await native.modelScript.releaseGateIfHeld(identity.gate)
      await retryUntilPass(async () => {
        const status = await native.modelScript.status()
        expect(status.ruleMatches[`[${identity.gate}] the native teammate completion notice`]).toBe(1)
      })
      // The native SDK flushes idle task events only when the next user turn starts.
      // TaskOutput checks the real task state before its completion reaches the row.
      const start = await native.modelScript.queue(
        {
          toolCalls: [
            codebuddyTaskOutputToolCall('native-swarm-task-output', task.id),
          ],
        },
        nativeTextStep(native, 'The native completed teammate task was read.'),
      )
      await sendMessage(native.page, native.modelScript.prompt('Read the native teammate task snapshot.'))
      await native.modelScript.waitForSteps(start + 2)
      await waitForAgentIdle(native.page)
      const result = nativeModelContextText(await native.modelScript.requestAt(start + 1))
      expect(result).toContain('Status: completed')
      expect(result).toContain('NATIVECHILDCOMPLETE')
      await child.finish()
    },
  }, { rowText: identity.description })
  const after = await currentNativeAgent(native)
  expectSameNativeSession(before, after, 'The native Swarm teammate')
  expect(nativeOptionValue(after, 'swarmMode')).toBe('true')
  for (const [id, value] of Object.entries(settings))
    expect(nativeOptionValue(after, id)).toBe(value)
  await testInfo.attach('codebuddy-native-swarm-parent', {
    body: JSON.stringify({
      before: { id: before.id, sessionId: before.agentSessionId, settings },
      after: {
        id: after.id,
        sessionId: after.agentSessionId,
        settings: {
          model: nativeOptionValue(after, 'model'),
          permissionMode: nativeOptionValue(after, 'permissionMode'),
        },
      },
      spawn,
      taskId: task.id,
    }),
    contentType: 'application/json',
  })
  await native.page.reload()
  await expectNativeOptionValue(native, 'swarmMode', 'true')
  await sendNativeAnswer(native, 'Reply once after the native teammate and reload.', 'The native parent remains usable after its teammate.')
  expectSameNativeSession(before, await currentNativeAgent(native), 'The native parent after teammate completion')
})

// Track the native route and its LeapMux integration: https://github.com/leapmux/leapmux/issues/523.
