export const siteURLs = {
  home: 'https://tokenswoop.xyz',
  mainnet: 'https://app.tokenswoop.xyz',
  testnet: 'https://testnet.tokenswoop.xyz',
} as const

type SiteLocation = {
  hostname: string
  pathname: string
  search: string
  hash: string
}
type SiteRoute = {
  workspace: boolean
  expectedChainID?: 143 | 10143
  canonicalPath?: string
  redirect?: string
}

function hostname(value: string) {
  return value.toLowerCase().replace(/\.$/, '')
}
function legacyWorkspacePath(path: string) {
  return path === '/app' || path.startsWith('/app/')
}

// Hostnames select the page, never the payment network. Workspace still loads
// its chain from the backend and verifies it matches the public hostname.
export function siteRoute(location: SiteLocation): SiteRoute {
  const host = hostname(location.hostname)
  const suffix = `/${location.search}${location.hash}`
  if (host === 'app.tokenswoop.xyz' || host === 'testnet.tokenswoop.xyz') {
    return {
      workspace: true,
      expectedChainID: host === 'app.tokenswoop.xyz' ? 143 : 10143,
      ...(legacyWorkspacePath(location.pathname)
        ? { canonicalPath: suffix }
        : {}),
    }
  }
  if (host === 'tokenswoop.xyz' || host === 'www.tokenswoop.xyz') {
    return {
      workspace: false,
      ...(legacyWorkspacePath(location.pathname)
        ? { redirect: siteURLs.mainnet + suffix }
        : {}),
    }
  }
  // Local development and private previews retain / and /app side by side.
  return { workspace: legacyWorkspacePath(location.pathname) }
}

export function homeHref(currentHostname: string) {
  const host = hostname(currentHostname)
  return [
    'tokenswoop.xyz',
    'www.tokenswoop.xyz',
    'app.tokenswoop.xyz',
    'testnet.tokenswoop.xyz',
  ].includes(host)
    ? siteURLs.home
    : '/'
}
