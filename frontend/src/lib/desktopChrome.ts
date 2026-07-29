export function hasWorkspaceDesktopChrome(pathname: string): boolean {
  // App home (`/`) is the only route with the workspace chrome (titlebar /
  // sidebar) — a workspace no longer has a path of its own, so `/` is where
  // every workspace is viewed. Auth and setup routes do not get the chrome.
  return pathname === '/' || pathname === ''
}
