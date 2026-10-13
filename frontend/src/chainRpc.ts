import { custom } from 'viem'
import { api } from './api.ts'

type RPCResponse = {
  result?: unknown
  error?: { code: number; message: string; data?: unknown }
}

// The endpoint credential stays on the server. Read the current session and
// CSRF cookie for every request, including after switching wallet accounts.
export function chainRpcTransport() {
  return custom(
    {
      async request({ method, params }) {
        const response = await api<RPCResponse>('/api/rpc', {
          method: 'POST',
          body: JSON.stringify({
            jsonrpc: '2.0',
            id: 1,
            method,
            params: params ?? [],
          }),
          signal: AbortSignal.timeout(18000),
        })
        if (response.error)
          throw Object.assign(new Error(response.error.message), response.error)
        return response.result
      },
    },
    // Pending transaction recovery already retries the exact signed bytes.
    { retryCount: 0 },
  )
}
