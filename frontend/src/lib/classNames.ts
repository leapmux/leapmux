/** Join present class names, or omit the attribute when none remain. */
export function joinClassNames(first: string, ...classes: Array<string | false | null | undefined>): string
export function joinClassNames(...classes: Array<string | false | null | undefined>): string | undefined
export function joinClassNames(...classes: Array<string | false | null | undefined>): string | undefined {
  return classes.filter((value): value is string => typeof value === 'string' && value.length > 0).join(' ') || undefined
}
