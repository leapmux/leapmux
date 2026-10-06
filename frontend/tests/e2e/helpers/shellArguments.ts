import { randomUUID } from 'node:crypto'

/**
 * Return `prefix` followed by 32 random hexadecimal digits.
 *
 * The marker holds only letters and digits. So a shell command holds it with no quote, a regular expression holds it
 * with no escape, and a JSON string holds it unchanged. A model script can then match the marker in a request body
 * without knowing how the provider encoded it.
 */
export function uniqueMarker(prefix = ''): string {
  if (!/^[A-Z0-9]*$/i.test(prefix))
    throw new Error(`A unique marker prefix holds only letters and digits, not ${JSON.stringify(prefix)}.`)
  return `${prefix}${randomUUID().replaceAll('-', '')}`
}

/** Quote one argument without executing shell substitutions or metacharacters. */
export function quotePosixShellArgument(value: string): string {
  return `'${value.replaceAll('\'', `'"'"'`)}'`
}

/**
 * A printf command that writes `<prefix><value>` and a newline to its standard output.
 *
 * printf joins its format and its argument, so the joined text occurs only in
 * the output of a command that ran, never in the command itself. A tool result
 * that echoes the command therefore cannot pass for its output.
 *
 * The command holds no `$(`, no backtick, and no `<(` or `>(`. Gemini CLI
 * refuses every command that holds one of them, arithmetic expansion such as
 * `$((40 + 2))` included (`detectBashSubstitution` in its shell tool). A probe
 * that computes its marker with `$((...))` therefore cannot run there.
 */
export function printfMarkerCommand(prefix: string, value: number): string {
  if (!/^\w[\w-]*$/.test(prefix))
    throw new Error('The printf marker prefix requires letters, digits, underscores, or hyphens only, and it must not start with a hyphen.')
  if (!Number.isSafeInteger(value))
    throw new Error('The printf marker value requires a safe integer.')
  return `printf '${prefix}%s\\n' ${value}`
}
