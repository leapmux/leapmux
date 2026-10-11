import { expect } from '@playwright/test'
import { chooseQuestionOption, exerciseQuestionAnswer, NATIVE_CONTROL_QUESTION } from '../helpers/nativeQuestion'
import { expectNoNativeEditorRequest } from '../helpers/unsupportedEditor'
import { kiloTest } from '../kilo-fixtures'

kiloTest('resolves an actual native control without exposing a multiline editor request', async ({ native }) => {
  await expectNoNativeEditorRequest(native, {
    relatedProof: async () => {
      const { result } = await exerciseQuestionAnswer(native, { questions: [NATIVE_CONTROL_QUESTION], callId: 'native-editor-limit-question', reply: chooseQuestionOption('Green') })
      expect(result).toContain('Green')
      expect(result).not.toContain('Blue')
    },
  })
})
