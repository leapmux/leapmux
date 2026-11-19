import { OPENCODE_NATIVE_EVENT, OPENCODE_NATIVE_FORM_FIELD_TYPE } from '~/generated/contracts/opencode-native-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'

export interface OpenCodeForm {
  title: string
  metadata: Record<string, unknown>
  fields: Record<string, unknown>[]
}

const FIELD_TYPES = new Set<string>(Object.values(OPENCODE_NATIVE_FORM_FIELD_TYPE))

/** Read the native form while preserving every field's constraints and defaults. */
export function openCodeForm(payload: Record<string, unknown>): OpenCodeForm | undefined {
  if (payload.type !== OPENCODE_NATIVE_EVENT.FormCreated)
    return undefined
  const form = pickObject(pickObject(payload, 'data'), 'form')
  if (!form || !Array.isArray(form.fields) || form.fields.length === 0)
    return undefined
  const fields = []
  for (const field of form.fields) {
    if (!isObject(field) || typeof field.key !== 'string' || !FIELD_TYPES.has(pickString(field, 'type')))
      return undefined
    fields.push(field)
  }
  return {
    title: pickString(form, 'title'),
    metadata: pickObject(form, 'metadata') ?? {},
    fields,
  }
}
