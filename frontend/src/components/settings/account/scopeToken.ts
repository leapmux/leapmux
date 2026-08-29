import { Scope } from '~/generated/leapmux/v1/scope_pb'

/**
 * One scope's RFC 6749 section 3.3 token, from the generated enum name.
 *
 * WORKSPACE_READ becomes workspace:read: the first underscore-separated part
 * is the family and the rest is the action. (The Go side's enum names carry a
 * SCOPE_ prefix; protoc-gen-es strips it, so the TS name starts at the
 * family.) The Go side builds its tokens from an explicit bijection
 * (grantableTokens in internal/authscope) rather than from the enum names, so
 * this derivation matches it by convention, not by construction --
 * TestScopeTokenBijectionMatchesEnumNames pins the two together on the Go
 * side, and fails there when a grantable name loses the FAMILY_ACTION shape.
 *
 * A name that does not follow the shape RENDERS RAW and logs, rather than
 * throwing: this runs inside Solid render paths (the permission checkboxes,
 * each registration's ceiling), and a throw here takes down the whole
 * Preferences dialog -- under only the app's outermost error boundary -- so
 * one future scope name without an underscore would blank the settings UI
 * instead of showing one unlabeled checkbox. The raw rendering is loud enough
 * to fail a review and honest enough to read; the Go-side test is what makes
 * the break fail CI before it ships.
 */
export function scopeToken(scope: Scope): string {
  const name = Scope[scope]
  if (typeof name !== 'string' || name.includes('SCOPE_')) {
    console.error(`scope enum value ${scope} carries no FAMILY_ACTION name`)
    return `scope-${scope}`
  }
  const parts = name.split('_')
  if (parts.length < 2 || parts.includes('')) {
    console.error(`scope name ${name} does not follow the FAMILY_ACTION shape its token is derived from`)
    return name.toLowerCase()
  }
  return `${parts[0]!.toLowerCase()}:${parts.slice(1).join('_').toLowerCase()}`
}
