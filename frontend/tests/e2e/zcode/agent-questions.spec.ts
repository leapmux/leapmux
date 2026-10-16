import { exerciseQuestionPages, exerciseQuestionPreviews, exerciseTypedQuestionAnswer } from '../helpers/nativeQuestion'
import { exerciseQuestionIsolation } from '../helpers/nativeQuestionIsolation'
import { zcodeTest } from '../zcode-fixtures'

zcodeTest('renders ZCode native question descriptions and selects an answer', async ({ native }) => {
  await exerciseQuestionPreviews(native)
})

zcodeTest('preserves answers across native question pages and reloads', async ({ native }) => {
  await exerciseQuestionPages(native)
})

zcodeTest('returns a custom native answer after its draft survives reload', async ({ native }) => {
  await exerciseTypedQuestionAnswer(native)
})

zcodeTest('keeps a native question on its owner tab and workspace after reload', async ({ native }) => {
  await exerciseQuestionIsolation(native, { customAnswerDraft: true })
})
