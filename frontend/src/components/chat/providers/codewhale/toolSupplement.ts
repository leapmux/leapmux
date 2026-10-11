import { CODEWHALE_SUPPLEMENT_FIELD } from '~/generated/contracts/codewhale-protocol'
import { isObject } from '~/lib/jsonPick'

/** Read recovered output file bytes without changing the native event. */
export function codewhaleToolOutputFiles(supplemental: unknown): Record<string, unknown> | undefined {
  if (!isObject(supplemental))
    return undefined
  const outputFiles = supplemental[CODEWHALE_SUPPLEMENT_FIELD.OutputFiles]
  return isObject(outputFiles) ? outputFiles : undefined
}
