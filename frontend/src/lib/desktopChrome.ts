export function hasWorkspaceDesktopChrome(pathname: string): boolean {
  // App home (`/`) and workspace detail (`/workspace/...`) share the
  // workspace chrome (titlebar / sidebar). Auth and setup routes do not.
  return pathname === '/' || pathname === '' || /^\/workspace(?:\/.*)?$/.test(pathname)
}
