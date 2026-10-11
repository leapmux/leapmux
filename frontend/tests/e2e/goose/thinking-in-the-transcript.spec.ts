import { gooseTest } from '../goose-fixtures'
import { MOCK_MODELS } from '../helpers/mockAgentEnvironment'
import { exerciseThinkingRows } from '../helpers/thinkingRows'
import { chooseSettingsOption } from '../helpers/ui'

gooseTest('keeps a thought after its streamed answer on reload', async ({ native }) => {
  await chooseSettingsOption(native.page, `model-${MOCK_MODELS.gooseReasoning}`)
  await exerciseThinkingRows(native, { order: 'after' })
})
