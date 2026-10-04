/** Quote one argument without executing shell substitutions or metacharacters. */
export function quotePosixShellArgument(value: string): string {
  return `'${value.replaceAll('\'', `'"'"'`)}'`
}
