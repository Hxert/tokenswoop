import openai from './assets/brands/openai.svg'
import anthropic from './assets/brands/anthropic.svg'
import gemini from './assets/brands/gemini.svg'
import aws from './assets/brands/aws.svg'
import azure from './assets/brands/azure.svg'
import monad from './assets/brands/monad.svg'
import privy from './assets/brands/privy.svg'
import quicknode from './assets/brands/quicknode.svg'
import mera from './assets/brands/mera.svg'
import usdc from './assets/brands/usdc.svg'

export type Brand = { id: string; name: string; src: string }

export const brands: Brand[] = [
  { id: 'openai', name: 'OpenAI', src: openai },
  { id: 'anthropic', name: 'Anthropic', src: anthropic },
  { id: 'gemini', name: 'Gemini', src: gemini },
  { id: 'aws', name: 'aws', src: aws },
  { id: 'azure', name: 'Azure', src: azure },
  { id: 'monad', name: 'Monad', src: monad },
  { id: 'privy', name: 'Privy', src: privy },
  { id: 'quicknode', name: 'QuickNode', src: quicknode },
  { id: 'mera', name: 'Mera', src: mera },
]

export const characterBrands: Brand[] = [
  { id: 'usdc', name: 'USDC', src: usdc },
  ...brands.filter((brand) =>
    ['openai', 'anthropic', 'gemini', 'monad'].includes(brand.id),
  ),
]

export const monadLogo = monad

export function modelBrand(vendor: string): Brand | undefined {
  const id = vendor
    .trim()
    .toLowerCase()
    .replace(/[\s_-]+/g, '')
  const aliases: Record<string, string> = {
    openai: 'openai',
    anthropic: 'anthropic',
    claude: 'anthropic',
    google: 'gemini',
    googleai: 'gemini',
    googlegemini: 'gemini',
    gemini: 'gemini',
  }
  return brands.find((brand) => brand.id === aliases[id])
}
