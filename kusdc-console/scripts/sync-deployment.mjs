import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const directory = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const manifest = JSON.parse(fs.readFileSync(path.join(directory, '../contracts/deployments/monad-testnet.json'), 'utf8'))
if (manifest.chainId !== 10143 || !/^0x[\da-fA-F]{40}$/.test(manifest.kUSDC) || manifest.tokenManagementVersion !== 1) {
  throw new Error('Expected the current Monad Testnet management-enabled kUSDC deployment')
}
fs.writeFileSync(path.join(directory, 'src/deployment.json'), JSON.stringify({ chainId: 10143, token: manifest.kUSDC, rpc: manifest.rpc }, null, 2) + '\n')
console.log('Standalone kUSDC console deployment synchronized.')
