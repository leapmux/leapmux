import { exerciseRestoredNativeOption } from '../helpers/nativeSettings'
import { chooseSettingsOption, sendMessage, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { expect, QODER_E2E_SKIP_REASON, qoderTest } from '../qoder-fixtures'
import { nativeContext } from './scenarios'

qoderTest.describe('Qoder CLI effort and session goal', () => {
  qoderTest.skip(!!QODER_E2E_SKIP_REASON, QODER_E2E_SKIP_REASON || '')

  qoderTest('uses the selected reasoning effort in the next native model request', async ({ qoderWorkspace, page, modelScript, leapmuxServer }) => {
    void qoderWorkspace
    await waitForSettingsHydrated(page)
    await modelScript.queue({ text: 'PRIOR_QODER_EFFORT_CONTEXT' })
    await sendMessage(page, modelScript.prompt('Remember the effort context marker.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)

    await chooseSettingsOption(page, 'effort-low')
    await waitForSettingsIdle(page)
    await expect(page.locator('[data-testid="composer-effort-trigger"]:visible')).toContainText('Low')

    await modelScript.queue({ text: 'LOW_EFFORT_APPLIED' })
    await sendMessage(page, modelScript.prompt('Reply once using the selected effort.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)
    const request = status.requests.find(item => item.stepIndex === 1)
    expect(request).toBeDefined()
    const body = JSON.stringify(request?.body)
    expect(request?.body).toMatchObject({ reasoning_effort: 'low' })
    expect(body.includes('PRIOR_QODER_EFFORT_CONTEXT')).toBe(true)

    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: qoderWorkspace.workspaceId })
    await exerciseRestoredNativeOption(context, {
      groupId: 'effort',
      value: 'low',
      nativeProof: (request) => {
        expect(request.body).toMatchObject({ reasoning_effort: 'low' })
      },
    })
    await expect(page.locator('[data-testid="composer-effort-trigger"]:visible')).toContainText('Low')
  })
})
