export function hasWorkspaceDesktopChrome(pathname: string): boolean {
  return /^\/o\/[^/]+(?:\/(?:workspace\/.*)?)?$/.test(pathname)
}
