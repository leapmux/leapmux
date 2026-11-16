import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { newNativeWorkingDir, openNativeAgent } from '../helpers/nativeAgentOpen'
import { nativeScenarioModelContextText, nativeToolOutcome } from '../helpers/nativeScenario'
import { runNativeToolTurn } from '../helpers/nativeToolExecution'
import { kimiSkillToolCall } from '../helpers/providerToolCalls'
import { expectCleanTranscript } from '../helpers/transcriptGuard'
import { messageContents, waitForAgentIdle } from '../helpers/ui'
import { kimiTest } from '../kimi-fixtures'
import { nativeContext } from './scenarios'

kimiTest('retains native skill activation facts across reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
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
  const notice = () => messageContents(native.page).filter({ hasText: 'Activated skill leapmux-native-review' })
  await expect(notice()).toHaveCount(1)
  await expect(notice()).toContainText('Arguments: staged changes')
  await expect(notice()).toContainText('Trigger: model-tool')
  await expect(notice()).toContainText(join(directory, 'SKILL.md'))
  await native.page.reload()
  await waitForAgentIdle(native.page)
  await expect(notice()).toHaveCount(1)
  await expect(notice()).toContainText('Arguments: staged changes')
  await expectCleanTranscript(native.page)
})
