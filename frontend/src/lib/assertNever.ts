/**
 * Exhaustiveness check for a discriminated union.
 *
 * Call it in the `default` arm of a `switch` over a union's discriminant. While
 * every member is handled, `value` narrows to `never` and the call type-checks;
 * add a member without handling it and the call becomes a compile error naming
 * the missed case.
 *
 * The runtime throw is a backstop for values that reach here despite the types
 * (an unvalidated payload, a stale bundle). Prefer it over returning a default:
 * a silent fallback is exactly the fallthrough this helper exists to prevent.
 */
export function assertNever(value: never): never {
  throw new Error(`unhandled union member: ${JSON.stringify(value)}`)
}
