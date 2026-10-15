export interface NativeWorkflowScriptCase {
  callId: string
  name: string
  source: string
  expected: string
  failed: boolean
}

/** Build paired success and error scripts whose results require JavaScript execution. */
export function nativeWorkflowScripts(marker: string): readonly NativeWorkflowScriptCase[] {
  if (!/^[a-z0-9]+$/i.test(marker))
    throw new Error('A native Workflow marker requires letters and digits.')
  return [false, true].map((failed) => {
    const label = failed ? 'error' : 'output'
    const name = `native-code-${label}-${marker.toLowerCase()}`
    return {
      callId: `workflow-code-${label}`,
      name,
      source: [
        `export const meta = { name: ${JSON.stringify(name)}, description: 'Compute one local value.' };`,
        failed ? `throw new Error(${JSON.stringify(marker)} + (70 + 7));` : `return ${JSON.stringify(marker)} + (40 + 2);`,
      ].join('\n'),
      expected: `${marker}${failed ? 77 : 42}`,
      failed,
    }
  })
}
