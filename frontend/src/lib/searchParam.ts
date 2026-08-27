/**
 * The single string a search parameter carries, or undefined.
 *
 * `useSearchParams` types every value as `string | string[] | undefined`,
 * because a URL may repeat a name: `?redirect=/a&redirect=/b` arrives as an
 * array. A repeated name is never a value this app means to act on, so the
 * array answers undefined and the caller falls back.
 *
 * It takes the VALUE, not the params proxy, so the property read stays in
 * the caller's own reactive scope.
 */
export function stringParam(value: string | string[] | undefined): string | undefined {
  return typeof value === 'string' ? value : undefined
}
