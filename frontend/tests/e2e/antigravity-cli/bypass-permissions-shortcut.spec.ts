import { expect } from '@playwright/test'
import { NOTIFICATION_TYPE } from '../../../src/generated/contracts/worker-vocab'
import { antigravityTest } from '../antigravity-fixtures'
import { exerciseBypassPermissions } from '../helpers/nativeBypassPermissions'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { exerciseMidTurnBypass } from '../helpers/nativeMidTurnBypass'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { messageContents } from '../helpers/ui'

antigravityTest('runs native tools without prompts after the bypass shortcut', async ({ native }) => {
  await exerciseBypassPermissions(native)
})

antigravityTest('approves a pending tool and the next tool when Bypass changes during a turn', async ({ native }) => {
  await exerciseMidTurnBypass(native)
  await expect(messageContents(native.page).filter({ hasText: 'Bypass permissions approved' })).toHaveCount(0)
  const { id } = await currentNativeAgent(native)
  const { messages } = await readNativeMessageSnapshot(native, id)
  expect(messages.filter(message => JSON.stringify(nativeMessageBody(message)).includes(NOTIFICATION_TYPE.PendingRequestsApproved))).toEqual([])
})
