import { expect } from '@playwright/test'
import { ZCODE_BACKGROUND_TURN_END_FIELD, ZCODE_EVENT, ZCODE_RECORD } from '../../../src/generated/contracts/zcode-protocol'
import { MessageCompletion } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject, pickString } from '../../../src/lib/jsonPick'
import { expectGoalStatus, scriptedObjective, setGoal } from '../helpers/goalsAndTodos'
import { expectNoUnrenderedRow } from '../helpers/nativeConversation'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { messageContents, visibleOnly } from '../helpers/ui'
import { zcodeTest } from '../zcode-fixtures'
import { NOT_PASSED_VERDICT, PASSED_VERDICT, zcodeVerifierRule } from './goalVerifier'

zcodeTest('session-goal-verification: renders both goal check verdicts and ends the achieved goal', async ({ native }) => {
  const { page, modelScript } = native
  // ZCode runs its goal turns with the objective as the prompt. The fallback
  // answers each one. The two verifier calls take the two rules in order.
  await modelScript.fallback({ text: 'Goal turn complete.' })
  // One call carries both rules. Each `rule` call PREPENDS its rules to the
  // scenario, so two calls would answer the first check with the second rule and
  // end the goal after one iteration. Inside one call the declaration order
  // holds, so the not-passed verdict answers the first check.
  await modelScript.rule(
    zcodeVerifierRule('zcode-goal-verifier-not-passed', NOT_PASSED_VERDICT, { once: true }),
    zcodeVerifierRule('zcode-goal-verifier-passed', PASSED_VERDICT, { once: true }),
  )

  await setGoal(page, scriptedObjective(modelScript, 'Run the goal loop until its verification passes.'))

  // Each check draws one row with its verdict. The row is the goal check's own
  // frame, so the wording states the iteration, the verdict and its reason.
  const rows = visibleOnly(messageContents(page))
  await expect(rows.filter({ hasText: 'Iteration 1 · Goal incomplete, continuing — The tests still fail. Next: Fix the failing test.' }).first()).toBeVisible()
  await expect(rows.filter({ hasText: 'Iteration 2 · Goal complete, ending task — All tests pass.' }).first()).toBeVisible()
  await expectGoalStatus(page, 'done')

  const backgroundEnd = rows.filter({ hasText: 'Background turn ended' }).first()
  await expect(backgroundEnd).toBeVisible()
  const agent = await currentNativeAgent(native)
  // The goal status arrives before the final native turn end. Wait for both
  // stored outcomes before checking their identities and completion values.
  const records = await retryUntilPass(async () => {
    const snapshot = await readNativeMessageSnapshot(native, agent.id)
    const records = snapshot.messages.flatMap((message) => {
      const body = nativeMessageBody(message)
      return isObject(body) && Array.isArray(body.messages)
        ? body.messages
        : [body]
    }).filter(isObject)
    expect(records.filter(record => record.type === ZCODE_RECORD.BackgroundTurnEnd)).toHaveLength(2)
    return records
  })
  // Setting the goal creates one native control-only foreground turn. The
  // two goal continuations retain separate background outcomes.
  const foreground = records.filter(record => record.type === ZCODE_EVENT.TurnCompleted)
  expect(foreground).toHaveLength(1)
  expect(foreground[0]).toMatchObject({
    payload: {
      response: '',
      tokenCount: 0,
      toolCallCount: 0,
      duration: 0,
      resultType: 'success',
    },
  })
  const background = records.filter(record => record.type === ZCODE_RECORD.BackgroundTurnEnd)
  expect(background).toHaveLength(2)
  const backgroundTurnIds = new Set<string>()
  for (const record of background) {
    if (!isObject(record.payload))
      throw new Error('The Worker stored a background outcome without its payload.')
    expect(record.payload[ZCODE_BACKGROUND_TURN_END_FIELD.Completion]).toBe(MessageCompletion.COMPLETE)
    const original: unknown = JSON.parse(pickString(record.payload, ZCODE_BACKGROUND_TURN_END_FIELD.Original))
    expect(original).toMatchObject({
      type: ZCODE_EVENT.TurnCompleted,
      payload: { resultType: 'success' },
    })
    if (!isObject(original))
      throw new Error('The Worker stored no native background end.')
    expect(original.turnId).not.toBe(foreground[0]?.turnId)
    backgroundTurnIds.add(pickString(original, 'turnId'))
  }
  expect(backgroundTurnIds.size).toBe(2)
  expect(backgroundTurnIds.has('')).toBe(false)
  await expect(visibleOnly(page.getByTestId('result-divider'))).toHaveCount(1)

  // The side queries stay off the transcript: the goal summary title and the
  // verifier's raw JSON verdict are the app-server's own model calls.
  await expect(rows.filter({ hasText: '{"title":' })).toHaveCount(0)
  await expect(rows.filter({ hasText: '{"passed":' })).toHaveCount(0)
  await expectNoUnrenderedRow(page)
  await page.reload()
  await expect(backgroundEnd).toBeVisible()
  await expect(visibleOnly(page.getByTestId('result-divider'))).toHaveCount(1)
  await expectNoUnrenderedRow(page)
})
