import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import type { ShellToolExecutionOptions } from '../helpers/nativeToolExecution'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'

/**
 * Run the shared shell scenario with an output gate on every command.
 *
 * MiMo Code 0.1.15 reads the output of a command in a fiber, and its bash tool
 * ends that fiber when the command exits (`BashTool.run`). A command that exits
 * right after it writes can lose its output, and the tool then answers
 * "(no output)". The gate holds each command until the live view shows its
 * output. The tool then has the output before the command ends.
 *
 * A MiMo spec that asserts the output of a shell command calls this function.
 * It does not call the shared scenario.
 */
export function exerciseMiMoShellToolExecution(
  context: ManagedNativeScenarioContext,
  options: Omit<ShellToolExecutionOptions, 'outputGate'> = {},
): Promise<void> {
  return exerciseShellToolExecution(context, { ...options, outputGate: true })
}
