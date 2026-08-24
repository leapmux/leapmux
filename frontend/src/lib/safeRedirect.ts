/**
 * Returns the redirect target only when it is a same-origin app path: an
 * absolute "/" path that is not protocol-relative ("//host"). Anything
 * else — an external URL, a protocol-relative URL, a malformed value — is
 * refused, so a post-auth navigate can never bounce the user to another
 * origin. The login and sign-up pages share this one implementation of
 * the open-redirect guard.
 */
export function safeRedirect(redirect: string | undefined): string | undefined {
  if (!redirect)
    return undefined
  // A protocol-relative target ("//host") or a backslash twin ("/\host",
  // which some browsers normalize to a scheme-relative URL) both leave the
  // origin; refuse anything but a plain same-origin app path.
  if (!redirect.startsWith('/') || redirect[1] === '/' || redirect[1] === '\\')
    return undefined
  return redirect
}
