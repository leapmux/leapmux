import { join } from 'node:path'
import { expect } from '@playwright/test'
import { CLINE_PERMISSION_MODE } from '../../../src/generated/contracts/cline-protocol'
import { clineTest } from '../cline-fixtures'
import { clineProviders } from '../helpers/clineEnvironment'
import { MODEL_KEY } from '../helpers/mockAgentEnvironment'
import { withNativeWorker } from '../helpers/nativeWorker'
import { writePrivateJSON } from '../helpers/privateConfigFile'
import { readToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { getGlobalState } from '../helpers/server'
import { expectToolRowImage, runToolImageTurn, toolResultImageForName } from '../helpers/toolImages'
import { openWorkspace, tabById } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { CLINE_AGENT, nativeContext } from './scenarios'

clineTest.describe('images in tool results for Cline', () => {
  clineTest('renders a native PNG read through the default profile before and after reload', async ({ native, authenticatedClineWorkspace }) => {
    await runToolImageTurn(native, {
      workingDir: authenticatedClineWorkspace.workingDir,
      marker: 'cline-19',
      toolCall: image => readToolCall(native.provider, 'read-png', image.path),
    })
    await expectToolRowImage(native.page, 'tool-image-cline-19')
    await native.page.reload()
    await expectToolRowImage(native.page, 'tool-image-cline-19')
  })

  clineTest('renders native read images with an image-capable model before and after reload', async ({ authenticatedEmptyWorkspace, leapmuxServer, page, modelScript }) => {
    const directory = createTestDirectory('cline-read-images-')
    const settings = join(directory, 'providers.json')
    const model = 'gpt-5.4'
    writePrivateJSON(settings, clineProviders({ providerID: 'openai-native', modelID: model, modelKey: MODEL_KEY, baseURL: `${getGlobalState().mockModelUrl}/v1` }, Date.now()))
    await withNativeWorker(leapmuxServer, {
      dataDirPrefix: 'cline-read-images-worker',
      workerName: 'cline-read-images',
      env: { CLINE_PROVIDER_SETTINGS_PATH: settings },
      privateDirectories: [directory],
    }, async (worker) => {
      const workspaceId = authenticatedEmptyWorkspace.workspaceId
      const opened = await openProviderAgent(worker.server, workspaceId, CLINE_AGENT, { model, optionValues: { permissionMode: CLINE_PERMISSION_MODE.AutoApprove } })
      await openWorkspace(page, workspaceId)
      const native = await nativeContext({ page, modelScript, leapmuxServer: worker.server, workspaceId })
      const image = await runToolImageTurn(native, {
        workingDir: opened.workingDir,
        marker: 'cline-native-read',
        toolCall: image => readToolCall(native.provider, 'read-native-image', image.path),
      })
      for (const reload of [false, true]) {
        if (reload)
          await page.reload()
        await expectToolRowImage(page, image.fileName)
        const picture = await toolResultImageForName(page, image.fileName)
        await picture.locator('xpath=ancestor::button[@aria-label="Open image"]').click()
        const viewer = page.locator('img[src^="blob:"]:visible')
        await expect(viewer).toBeVisible()
        await expect.poll(() => viewer.evaluate(element => element instanceof HTMLImageElement ? [element.naturalWidth, element.naturalHeight] : [])).toEqual([64, 64])
        await tabById(page, opened.agentId).click()
      }
    })
  })
})
