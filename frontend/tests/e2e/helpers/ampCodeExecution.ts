import { runInNewContext } from 'node:vm'

/** Amp executes this tool on its service, outside the local CLI executor. */
export const AMP_CODE_EXEC_TOOL = 'code_exec'

const SCRIPT_EXECUTION_LIMIT_MS = 1000

export type AmpCodeExecutionRun
  = { status: 'done', result: string }
    | { status: 'error', error: { message: string } }

/**
 * Model the remote service's synchronous JavaScript and text() output for isolated browser tests.
 * Native probes separately establish Amp execution. This fixture tests the CLI and LeapMux result path.
 * These scenarios require only computed values and script errors. The fixture does not model remote tool calls.
 */
export function executeAmpCodeFixture(code: unknown): AmpCodeExecutionRun {
  if (typeof code !== 'string' || !code.trim())
    return { status: 'error', error: { message: 'The Amp code fixture requires nonempty JavaScript.' } }
  const output: string[] = []
  try {
    runInNewContext(code, {
      text: (value: unknown) => {
        const text = typeof value === 'string' ? value : JSON.stringify(value)
        if (text !== undefined)
          output.push(text)
      },
    }, { timeout: SCRIPT_EXECUTION_LIMIT_MS })
    return { status: 'done', result: output.join('\n') }
  }
  catch (error) {
    return { status: 'error', error: { message: String(error) } }
  }
}
