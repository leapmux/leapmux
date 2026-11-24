import type { JsonValue } from '@bufbuild/protobuf'
import type { ElicitationRequest } from '../../model/controlPrompt'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { fromJson } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { ControlPromptSchema } from '~/generated/proto/leapmux/v1/agent_control_pb'
import { createElicitationForm, elicitationFieldKey } from '../../controls/elicitationForm'
import { controlPromptFromWire } from '../../model/controlPromptWire'

/**
 * The browser's half of testdata/opencode_form_prompt_conformance.json.
 *
 * Each case is a native OpenCode form and the elicitation prompt that the Worker
 * records for it. The Worker's half (`TestReadControlRequest_FormPromptConformance`)
 * proves that the OpenCode provider reads the form into that prompt. This half proves
 * that the shared form shows and sends what the form asks for, from that prompt alone.
 */
interface FormPromptCase {
  name: string
  prompt: JsonValue
  visible?: { answers: Record<string, string>, fields: string[] }[]
  reads?: { answers: Record<string, string>, content?: Record<string, unknown>, error?: string | true }[]
}

const fixturePath = resolve(dirname(fileURLToPath(import.meta.url)), '../../../../../../testdata/opencode_form_prompt_conformance.json')
const fixture = JSON.parse(readFileSync(fixturePath, 'utf8')) as { cases: FormPromptCase[] }

/** The elicitation of one case's recorded prompt, in the model that the form reads. */
function elicitationOf(entry: FormPromptCase): ElicitationRequest {
  const prompt = controlPromptFromWire(fromJson(ControlPromptSchema, entry.prompt))
  if (prompt.kind !== 'elicitation')
    throw new Error(`the case ${entry.name} records a ${prompt.kind} prompt, not an elicitation`)
  return prompt.elicitation
}

/** The values of the form's inputs for answers keyed by field. */
function inputValues(answers: Record<string, string>): Record<string, string> {
  return Object.fromEntries(Object.entries(answers).map(([key, value]) => [
    elicitationFieldKey(key),
    value,
  ]))
}

describe('opencode form prompt conformance', () => {
  it('holds cases', () => {
    expect(fixture.cases.length).toBeGreaterThan(0)
  })

  it.each(fixture.cases)('shows the fields of $name', (entry) => {
    const elicitation = elicitationOf(entry)
    const form = createElicitationForm(elicitation.schema, elicitation.fieldRules)
    for (const visible of entry.visible ?? [])
      expect(form.visibleFields(inputValues(visible.answers)).map(field => field.key)).toEqual(visible.fields)
  })

  it.each(fixture.cases)('sends the answers of $name', (entry) => {
    const elicitation = elicitationOf(entry)
    const form = createElicitationForm(elicitation.schema, elicitation.fieldRules)
    for (const read of entry.reads ?? []) {
      const result = form.read(inputValues(read.answers))
      if (read.error === true)
        expect(result.error).toBeTruthy()
      else if (read.error !== undefined)
        expect(result.error).toBe(read.error)
      else
        expect(result).toEqual({ content: read.content })
    }
  })
})
