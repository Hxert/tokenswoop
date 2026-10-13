import Landing from './Landing'
import Console from './Console'
import './console.css'
import WalletProvider from './WalletProvider'
import { useResource } from './api'
import type { Config } from './api'
import { configureNetwork } from './network'
import { Loading, Notice } from './ui'
import { useEffect } from 'react'
import { siteRoute } from './siteRouting'

export default function App() {
  const route = siteRoute(window.location)
  useEffect(() => {
    if (route.redirect) window.location.replace(route.redirect)
    else if (route.canonicalPath)
      window.history.replaceState(window.history.state, '', route.canonicalPath)
  }, [route.redirect, route.canonicalPath])
  if (route.redirect) return <Loading />
  return route.workspace ? (
    <Workspace expectedChainID={route.expectedChainID} />
  ) : (
    <Landing />
  )
}

function Workspace({ expectedChainID }: { expectedChainID?: 143 | 10143 }) {
  const config = useResource<Config>('/api/config')
  if (config.loading) return <Loading />
  if (config.error || !config.data)
    return (
      <Notice error>
        {config.error || 'Network configuration is unavailable.'}
      </Notice>
    )
  if (expectedChainID && config.data.chain_id !== expectedChainID)
    return (
      <Notice error>
        This workspace is connected to the wrong network. Please try again
        later.
      </Notice>
    )
  try {
    configureNetwork(config.data.chain_id)
  } catch (error) {
    return <Notice error>{(error as Error).message}</Notice>
  }
  return (
    <WalletProvider>
      <Console />
    </WalletProvider>
  )
}
