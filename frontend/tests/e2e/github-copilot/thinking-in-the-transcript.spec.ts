import { copilotTest } from '../copilot-fixtures'
import { MOCK_MODELS } from '../helpers/mockAgentEnvironment'
import { exerciseThinkingRows } from '../helpers/thinkingRows'
import { chooseSettingsOption } from '../helpers/ui'

copilotTest('keeps a thought before its answer after reload', async ({ native }) => {
  await chooseSettingsOption(native.page, `model-${MOCK_MODELS.gooseReasoning}`)
  await exerciseThinkingRows(native)
})
