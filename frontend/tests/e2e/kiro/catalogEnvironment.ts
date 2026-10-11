import { isObject } from '../../../src/lib/jsonPick'

/** Keep the complete active native registry in one private mock-only Worker. */
export function kiroCatalogEnvironment(environment: unknown): Record<string, string> {
  if (!isObject(environment) || typeof environment.KIRO_HOME !== 'string' || !environment.KIRO_HOME.trim())
    throw new Error('The native Kiro catalog requires an isolated profile environment.')
  const entries = Object.entries(environment).map(([key, value]): [string, string] => {
    if (typeof value !== 'string')
      throw new Error('The native Kiro catalog environment contains a non-string value.')
    return [key, value]
  })
  return Object.fromEntries([...entries, ['KIRO_FEATURE_TOOL_LOAD_ENABLED', 'false']])
}
