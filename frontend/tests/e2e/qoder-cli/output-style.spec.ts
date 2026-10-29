import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { expect } from '@playwright/test'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { nativeModelInstructionText } from '../helpers/nativeScenario'
import { exerciseNativeOptionSequence, expectSettingsOptionsOffered } from '../helpers/nativeSettings'
import { QODER_PROBE_OUTPUT_STYLE } from '../helpers/qoderEnvironment'
import { waitForNativeSettingsHydrated } from '../helpers/ui'
import { qoderTest } from '../qoder-fixtures'

/** The Output Style group of the Qoder provider. */
const OUTPUT_STYLE = 'outputStyle'

/** The instruction that each non-default style puts into the system prompt. The default style puts none of them. */
const STYLE_INSTRUCTIONS: Readonly<Record<string, string>> = {
  Explanatory: '# Explanatory Style Active',
  Learning: '# Learning Style Active',
  [QODER_PROBE_OUTPUT_STYLE.name]: QODER_PROBE_OUTPUT_STYLE.marker,
}

/** Require the instruction of `style` in the system prompt of the request, and no instruction of another style. */
function expectNativeOutputStyle(request: MockModelRequestRecord, style: string): void {
  const instructions = nativeModelInstructionText(request)
  for (const [name, instruction] of Object.entries(STYLE_INSTRUCTIONS)) {
    if (name === style)
      expect(instructions, `the system prompt carries the ${name} style`).toContain(instruction)
    else
      expect(instructions, `the system prompt carries no ${name} style`).not.toContain(instruction)
  }
}

qoderTest('selects each native output style for the next request, before and after reload', async ({ native }) => {
  // The native session takes a launch flag after its first turn, as the effort spec states.
  await sendNativeAnswer(native, 'Reply once before the output style changes.', 'The first turn answered.')
  await waitForNativeSettingsHydrated(native.page)
  // Qoder's initialize reply lists its built-in styles and the user style of the fixture.
  await expectSettingsOptionsOffered(native.page, OUTPUT_STYLE, [
    'auto',
    'default',
    'Explanatory',
    'Learning',
    QODER_PROBE_OUTPUT_STYLE.name,
  ])
  await exerciseNativeOptionSequence(native, {
    groupId: OUTPUT_STYLE,
    steps: [
      // Auto sends no flag, and the fixture's Qoder settings choose no style, so the default style runs.
      { value: 'auto', via: 'default' },
      { value: 'Explanatory', via: 'choose' },
      { value: 'Explanatory', via: 'reload' },
      { value: 'Learning', via: 'choose' },
      { value: QODER_PROBE_OUTPUT_STYLE.name, via: 'choose' },
      { value: 'default', via: 'choose' },
      { value: 'auto', via: 'choose' },
    ],
    nativeProof: (request, step) => {
      expectNativeOutputStyle(request, step.value)
    },
  })
})
