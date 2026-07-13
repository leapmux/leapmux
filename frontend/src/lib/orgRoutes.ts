/**
 * Route helpers for the `/o/{orgSlug}` space.
 *
 * The org-home path is built at several post-auth navigation sites (login,
 * signup, setup, email verification, OAuth completion, and the root redirect),
 * every one landing the user on their org home. Centralizing the scheme here
 * keeps those sites from drifting and names the intent at each call.
 */

/**
 * The path to an org's home, given its slug. The slug is the org name (which
 * mirrors the store-normalized username) or an equivalent username-derived
 * slug -- whatever the caller resolved for the org the user is landing on.
 */
export function orgHomePath(orgSlug: string): string {
  return `/o/${orgSlug}`
}
