import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { kimiConversationRow } from '../../../src/components/chat/providers/kimi/conversation'
import { newNativeWorkingDir, openNativeAgent } from '../helpers/nativeAgentOpen'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent, nativeScenarioModelContextText, nativeToolOutcome } from '../helpers/nativeScenario'
import { runNativeToolTurn } from '../helpers/nativeToolExecution'
import { kimiSkillToolCall } from '../helpers/providerToolCalls'
import { expectCleanTranscript } from '../helpers/transcriptGuard'
import { messageContents, toolCallRow, waitForAgentIdle } from '../helpers/ui'
import { kimiTest } from '../kimi-fixtures'
import { nativeContext } from './scenarios'

kimiTest('retains public native skill execution facts across reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const native = await nativeContext({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: authenticatedEmptyWorkspace.workspaceId,
  })
  const workingDir = newNativeWorkingDir(native, 'kimi-skill-activation-')
  const directory = join(workingDir, '.agents', 'skills', 'leapmux-native-review')
  mkdirSync(directory, { recursive: true })
  writeFileSync(join(directory, 'SKILL.md'), '---\nname: leapmux-native-review\ndescription: Review the staged changes for this test.\n---\nReport NATIVE_SKILL_CONTENT_9246 and inspect $ARGUMENTS.\n')
  await openNativeAgent(native, { workingDir })
  const { resultRequest } = await runNativeToolTurn(native, {
    prompt: 'Activate the project review skill with arguments staged changes.',
    toolCalls: [
      kimiSkillToolCall('activate-review', 'leapmux-native-review', 'staged changes'),
    ],
    answer: 'The project skill was activated.',
  })
  expect((await nativeToolOutcome(native, resultRequest, 'activate-review')).text).toContain('loaded inline')
  expect(nativeScenarioModelContextText(native, resultRequest)).toContain('NATIVE_SKILL_CONTENT_9246')
  // The 2.6.2 ConversationFeed omits SkillActivated from RECORD_EVENTS, and
  // conversationFold filters model-tool activation context. The public feed
  // exposes the tool call, not its activation path, source or trigger.
  const current = await currentNativeAgent(native)
  for (const reload of [false, true]) {
    if (reload) {
      await native.page.reload()
      await waitForAgentIdle(native.page)
    }
    const snapshot = await readNativeMessageSnapshot(native, current.id)
    const rows = snapshot.messages.flatMap((message) => {
      const row = kimiConversationRow(nativeMessageBody(message))
      return row?.kind === 'item' && row.item.type === 'tool_call' && row.item.item_id === 'activate-review'
        ? [row]
        : []
    })
    expect(rows).toHaveLength(2)
    expect(rows.map(row => ({
      session: row.sessionId,
      agent: row.agentId,
      role: row.role,
      name: row.item.name,
      input: row.item.input,
    }))).toEqual(['request', 'result'].map(role => ({
      session: snapshot.agentSessionId,
      agent: 'main',
      role,
      name: 'Skill',
      input: { skill: 'leapmux-native-review', args: 'staged changes' },
    })))
    expect(rows[1]?.item).toMatchObject({
      status: 'ended',
      outcome: 'completed',
      display: { kind: 'skill_call', skill_name: 'leapmux-native-review', args: 'staged changes' },
      output: 'Skill "leapmux-native-review" loaded inline. Follow its instructions.',
    })
    await expect(toolCallRow(native.page, 'activate-review', 'request')).toContainText('Skill: /leapmux-native-review')
    await expect(toolCallRow(native.page, 'activate-review')).toContainText('loaded inline')
    await expect(messageContents(native.page).filter({ hasText: 'Activated skill leapmux-native-review' })).toHaveCount(0)
    await expectCleanTranscript(native.page)
  }
})
