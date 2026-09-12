import type { Schema } from '@cfworker/json-schema'
import type { McpElicitationAction } from '~/generated/contracts/mcp-elicitation'
import { Validator } from '@cfworker/json-schema'
import { MCP_ELICITATION_ACTION } from '~/generated/contracts/mcp-elicitation'
import { isObject, pickString } from '~/lib/jsonPick'

export interface ElicitationRequest {
  mode: string
  title?: string
  description?: string
  message: string
  server?: string
  schema?: unknown
  url?: string
  arguments?: unknown
  argumentNotice?: string
  acceptChoices?: { key: string, label: string, metadata?: Record<string, unknown> }[]
}

export interface ElicitationOption {
  value: string
  label: string
}

export interface ElicitationField {
  key: string
  label: string
  description: string
  type: 'text' | 'number' | 'select' | 'multiple' | 'json'
  options: ElicitationOption[]
  initial: string
  hasDefault: boolean
  required: boolean
}

export interface ElicitationForm {
  fields: ElicitationField[]
  read: (values: Record<string, string>) => { content?: Record<string, unknown>, error?: string }
}

function enumOptions(schema: Record<string, unknown>): ElicitationOption[] {
  if (Array.isArray(schema.enum)) {
    const labels = Array.isArray(schema.enumNames) ? schema.enumNames : []
    return schema.enum.map((value, index) => ({ value: JSON.stringify(value), label: typeof labels[index] === 'string' ? labels[index] : String(value) }))
  }
  const variants = Array.isArray(schema.oneOf) ? schema.oneOf : Array.isArray(schema.anyOf) ? schema.anyOf : []
  if (variants.length > 0 && variants.every(item => isObject(item) && Object.hasOwn(item, 'const'))) {
    return variants.map(item => ({ value: JSON.stringify(item.const), label: pickString(item, 'title', String(item.const)) }))
  }
  return []
}

function fieldFor(key: string, schema: unknown, required: boolean): ElicitationField {
  const definition = isObject(schema) ? schema : {}
  const multiple = definition.type === 'array' && isObject(definition.items)
  let options = enumOptions(multiple ? definition.items as Record<string, unknown> : definition)
  if (definition.type === 'boolean')
    options = [{ value: 'true', label: 'Yes' }, { value: 'false', label: 'No' }]
  const type = options.length > 0
    ? multiple ? 'multiple' : 'select'
    : definition.type === 'string' ? 'text' : definition.type === 'number' || definition.type === 'integer' ? 'number' : 'json'
  const initial = definition.default === undefined ? '' : type === 'text' ? String(definition.default) : JSON.stringify(definition.default)
  return { key, label: pickString(definition, 'title', key), description: pickString(definition, 'description', ''), type, options, initial, hasDefault: Object.hasOwn(definition, 'default'), required }
}

/** Keep form state in the existing durable control-answer record. */
export function elicitationFieldKey(key: string): string {
  return `elicitation:${JSON.stringify(key)}`
}

/** Build typed inputs while the validator retains the complete provider schema. */
export function createElicitationForm(schema: unknown): ElicitationForm {
  const definition = isObject(schema) ? schema : undefined
  const properties = definition && isObject(definition.properties) ? definition.properties : undefined
  const required = definition && Array.isArray(definition.required) ? definition.required : []
  const fields = properties
    ? Object.entries(properties).map(([key, value]) => fieldFor(key, value, required.includes(key)))
    : [fieldFor('', {}, true)]
  let validator: Validator | undefined
  let schemaError: string | undefined
  try {
    if (!definition && typeof schema !== 'boolean')
      throw new Error('Could not read the form schema.')
    // Ajv compiles schemas with new Function. The production content security policy forbids it.
    // The validator adds lookup metadata. JSON serialization also copies reactive store proxies,
    // which structuredClone rejects. The provider schema contains JSON values only.
    validator = new Validator(JSON.parse(JSON.stringify(schema)) as Schema | boolean, '2020-12')
  }
  catch {
    schemaError = 'Could not read the form schema.'
  }
  return {
    fields,
    read(values) {
      if (!validator)
        return { error: schemaError }
      try {
        const entries: [string, unknown][] = []
        for (const field of fields) {
          const key = elicitationFieldKey(field.key)
          const value = values[key] ?? field.initial
          if (value === '' && (field.type !== 'text' || (!Object.hasOwn(values, key) && !field.hasDefault)))
            continue
          let parsed: unknown = value
          if (field.type !== 'text') {
            try {
              parsed = JSON.parse(value, (_key, value: unknown) => {
                if (typeof value === 'number' && (!Number.isFinite(value) || (Number.isInteger(value) && !Number.isSafeInteger(value))))
                  throw new RangeError('The number cannot be represented exactly.')
                return value
              })
            }
            catch (error) {
              if (error instanceof RangeError)
                return { error: `${field.label || 'Response'}: This number is too large to send without changing its value.` }
              return { error: `${field.label || 'Response'}: Enter valid ${field.type === 'number' ? 'number' : 'JSON'}.` }
            }
          }
          entries.push([field.key, parsed])
        }
        const content: unknown = properties ? Object.fromEntries(entries) : entries[0]?.[1]
        if (!isObject(content))
          return { error: 'Enter a JSON object.' }
        for (const key of required) {
          if (typeof key === 'string' && !Object.hasOwn(content, key))
            return { error: `${fields.find(field => field.key === key)?.label || key} is required.` }
        }
        const result = validator.validate(content)
        if (!result.valid) {
          const first = result.errors[0]
          return { error: first ? `${first.instanceLocation === '#' ? 'Response' : first.instanceLocation}: ${first.error.replace(/^Instance /, 'The value ')}` : 'Check the form values.' }
        }
        return { content }
      }
      catch {
        return { error: 'The form schema could not validate this response.' }
      }
    },
  }
}

/** URL interactions open only explicit HTTP or HTTPS links. */
export function elicitationURL(value: string | undefined): string | undefined {
  try {
    const url = new URL(value ?? '')
    if ((url.protocol === 'https:' || url.protocol === 'http:') && !url.username && !url.password)
      return url.href
  }
  catch {
    return undefined
  }
  return undefined
}

/** The worker restores the provider's original response identifier. */
export function buildElicitationResponse(requestId: string, action: McpElicitationAction, content?: Record<string, unknown>, metadata?: Record<string, unknown>): Record<string, unknown> {
  return {
    type: 'control_response',
    response: { subtype: 'success', request_id: requestId, response: { action, ...(action === MCP_ELICITATION_ACTION.Accept && content ? { content } : {}), ...(action === MCP_ELICITATION_ACTION.Accept && metadata ? { _meta: metadata } : {}) } },
  }
}

export const ELICITATION_ACCEPT_CHOICE = 'elicitation-accept-choice'

/** Apply only an approval choice that this request offers. */
export function elicitationAcceptMetadata(request: ElicitationRequest, choices: Record<string, string>): Record<string, unknown> | undefined {
  return request.acceptChoices?.find(choice => choice.key === choices[ELICITATION_ACCEPT_CHOICE])?.metadata
}
