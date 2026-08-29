/**
 * Returns the redirect target only when it is a same-origin app path: an
 * absolute "/" path that is not protocol-relative ("//host"). Anything
 * else — an external URL, a protocol-relative URL, a malformed value — is
 * refused, so a post-auth navigate can never bounce the user to another
 * origin. The login and sign-up pages share this one implementation of
 * the open-redirect guard.
 *
 * The three rules match `sanitizeRedirectURI` in
 * `backend/internal/hub/service/idp_handler.go`, which guards the same
 * value at the stronger sink (a Location header). Keep them together: the
 * hub forwards this parameter into the OAuth start URL, so a spelling one
 * side refuses and the other accepts is an open redirect.
 */
export function safeRedirect(redirect: string | undefined): string | undefined {
  if (!redirect || !redirect.startsWith('/'))
    return undefined
  // A protocol-relative target ("//host") or a backslash twin ("/\host",
  // which a WHATWG parser reads as scheme-relative) both leave the origin.
  if (redirect[1] === '/' || redirect[1] === '\\')
    return undefined
  // A control byte survives a header write and the browser strips it, so
  // "/\t/host" becomes "//host" at parse time. Refuse the whole class.
  // eslint-disable-next-line no-control-regex
  if (/[\u0000-\u001F\u007F]/.test(redirect))
    return undefined
  return redirect
}
