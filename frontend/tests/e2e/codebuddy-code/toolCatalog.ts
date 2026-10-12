import { isObject } from '../../../src/lib/jsonPick'

/** Require the native REPL's actual JavaScript code parameter and optional native parameter types. */
export function codebuddyReplSchema(schema: unknown): Record<string, unknown> {
  if (!isObject(schema) || schema.type !== 'object' || !isObject(schema.properties) || !Array.isArray(schema.required)
    || !schema.required.includes('code') || !isObject(schema.properties.code) || schema.properties.code.type !== 'string'
    || (schema.properties.timeout !== undefined && (!isObject(schema.properties.timeout) || schema.properties.timeout.type !== 'number'))
    || (schema.properties.description !== undefined && (!isObject(schema.properties.description) || schema.properties.description.type !== 'string'))) {
    throw new Error('The native CodeBuddy REPL discovery lacks its actual JavaScript code schema.')
  }
  return schema
}

/** Native ToolSearch can substitute a keyword match when an exact tool is unavailable. */
export function parseCodebuddyReplDiscovery(text: string): Record<string, unknown> {
  if (!text.startsWith('Found 1 tool(s). Use DeferExecuteTool to invoke them.\n\n## REPL\n'))
    throw new Error('The native CodeBuddy discovery did not return exactly the REPL tool.')
  const schemas = [...text.matchAll(/^Parameters:\n```json\n([\s\S]*?)\n```(?:\n|$)/gm)]
  if (schemas.length !== 1 || !text.trimEnd().endsWith('```'))
    throw new Error('The native CodeBuddy REPL discovery contains no unique complete input schema.')
  return codebuddyReplSchema(JSON.parse(schemas[0]![1]!))
}
