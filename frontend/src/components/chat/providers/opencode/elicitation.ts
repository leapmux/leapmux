import type { ElicitationFieldRule, ElicitationRequest } from '../../model/controlPrompt'
import { MCP_ELICITATION_MODE } from '~/generated/contracts/mcp-elicitation'
import { OPENCODE_NATIVE_FORM_FIELD_TYPE } from '~/generated/contracts/opencode-native-protocol'
import { isObject, pickString } from '~/lib/jsonPick'
import { openCodeIsQuestionRequest } from './askUserQuestion'
import { openCodeForm } from './form'

/** Read native typed forms through the shared schema-based elicitation surface. */
export function openCodeElicitation(payload: Record<string, unknown>): ElicitationRequest | undefined {
  const form = openCodeForm(payload)
  if (!form || openCodeIsQuestionRequest(payload))
    return undefined
  const server = pickString(form.metadata, 'server')
  const message = pickString(form.metadata, 'message')
  const base = {
    title: form.title,
    message,
    ...(server
      ? { server }
      : {}),
  }
  const external = form.fields.filter(field => field.type === OPENCODE_NATIVE_FORM_FIELD_TYPE.External)
  if (external.length === 1 && form.fields.length === 1) {
    return {
      ...base,
      mode: MCP_ELICITATION_MODE.URL,
      url: pickString(external[0], 'url'),
    }
  }
  const properties = new Map<string, unknown>()
  const required: string[] = []
  const fieldRules: ElicitationFieldRule[] = []
  for (const field of form.fields) {
    const key = pickString(field, 'key')
    fieldRules.push({
      key,
      hidden: field.hidden === true,
      conditions: Array.isArray(field.when)
        ? field.when.flatMap(condition => isObject(condition) && typeof condition.key === 'string'
          && (condition.op === 'eq' || condition.op === 'neq')
          && (typeof condition.value === 'string' || typeof condition.value === 'number' || typeof condition.value === 'boolean')
            ? [
                {
                  field: condition.key,
                  comparison: condition.op === 'eq'
                    ? 'equal' as const
                    : 'not-equal' as const,
                  value: condition.value,
                },
              ]
            : [])
        : [],
    })
    const schema: Record<string, unknown> = {}
    for (const name of [
      'title',
      'description',
      'default',
      'format',
      'minimum',
      'maximum',
      'minLength',
      'maxLength',
      'pattern',
    ]) {
      if (Object.hasOwn(field, name))
        schema[name] = field[name]
    }
    switch (field.type) {
      case OPENCODE_NATIVE_FORM_FIELD_TYPE.String:
      case OPENCODE_NATIVE_FORM_FIELD_TYPE.Number:
      case OPENCODE_NATIVE_FORM_FIELD_TYPE.Integer:
      case OPENCODE_NATIVE_FORM_FIELD_TYPE.Boolean:
        schema.type = field.type
        break
      case OPENCODE_NATIVE_FORM_FIELD_TYPE.Multiselect:
        schema.type = 'array'
        schema.items = { type: 'string' }
        for (const name of ['minItems', 'maxItems']) {
          if (Object.hasOwn(field, name))
            schema[name] = field[name]
        }
        break
      case OPENCODE_NATIVE_FORM_FIELD_TYPE.External:
        schema.type = 'boolean'
        schema.const = true
        schema.description = [
          pickString(field, 'description'),
          pickString(field, 'url'),
        ].filter(Boolean).join('\n\n')
        break
    }
    if (field.type === OPENCODE_NATIVE_FORM_FIELD_TYPE.String && field.required === true) {
      schema.minLength = Math.max(1, typeof schema.minLength === 'number'
        ? schema.minLength
        : 0)
    }
    if (field.type === OPENCODE_NATIVE_FORM_FIELD_TYPE.Multiselect && field.required === true) {
      schema.minItems = Math.max(1, typeof schema.minItems === 'number'
        ? schema.minItems
        : 0)
    }
    if (field.custom !== true && Array.isArray(field.options)) {
      const options = field.options.filter(isObject)
      const choices = {
        type: 'string',
        enum: options.map(option => option.value),
        enumNames: options.map(option => option.label),
      }
      if (field.type === OPENCODE_NATIVE_FORM_FIELD_TYPE.Multiselect)
        schema.items = choices
      else
        Object.assign(schema, choices)
    }
    properties.set(key, schema)
    if (field.required === true || field.type === OPENCODE_NATIVE_FORM_FIELD_TYPE.External)
      required.push(key)
  }
  return {
    ...base,
    mode: MCP_ELICITATION_MODE.Form,
    fieldRules,
    schema: {
      type: 'object',
      properties: Object.fromEntries(properties),
      required,
    },
  }
}
