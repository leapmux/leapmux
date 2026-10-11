import { gooseTest } from '../goose-fixtures'
import { goosePermissionJudgmentToolCall, gooseReadImageToolCall } from '../helpers/providerToolCalls'
import { expectDecodedImageInBubble, expectImageDataUriInRequest, runToolImageTurn } from '../helpers/toolImages'
import { messageBubbles } from '../helpers/ui'

gooseTest('shows the picture returned by read_image', async ({ native, authenticatedGooseWorkspace }) => {
  await native.modelScript.rule({
    name: 'the permission judge clears the image read',
    when: { system: 'permission-safety classifier' },
    respond: { toolCalls: [goosePermissionJudgmentToolCall('judge-goose-image', ['goose-read-image'])] },
  })
  const { fileName, resultRequest } = await runToolImageTurn(native, {
    workingDir: authenticatedGooseWorkspace.workingDir,
    marker: 'goose',
    approve: true,
    toolCall: image => gooseReadImageToolCall('goose-read-image', image.path),
  })
  expectImageDataUriInRequest(resultRequest, 'image/png')
  const nativeResult = messageBubbles(native.page)
    .filter({ hasText: 'Loaded image from' })
    .filter({ hasText: fileName })
    .first()
  await expectDecodedImageInBubble(nativeResult)
})
