import { isMeraError } from '@category-labs/mera'

// Mera retains the native WebAuthn exception as its cause. Do not turn every
// browser failure into a cancellation: that hides origin and provider errors.
export function passkeyError(error: unknown, creating: boolean): Error {
  if (!isMeraError(error))
    return error instanceof Error ? error : new Error('Passkey operation failed.')
  if (error.code === 'PRF_UNAVAILABLE')
    return new Error(
      'This passkey provider does not support wallet recovery (PRF). Use a provider with PRF support. If you just saved a passkey, try “Log in with passkey” before creating another.',
      { cause: error },
    )
  if (error.code !== 'PASSKEY_OPERATION_FAILED') return error

  let cause: unknown = error
  for (let depth = 0; depth < 5; depth++) {
    if (!(cause instanceof Error) || !cause.cause || cause.cause === cause) break
    cause = cause.cause
  }
  const name = cause instanceof Error ? cause.name : 'UnknownError'
  const saved = creating && error.message === 'Passkey assertion failed'
  const prefix = saved
    ? 'Your passkey was saved, but wallet setup could not finish. Choose “Log in with passkey” to continue with the same passkey.'
    : creating
      ? 'Passkey creation did not complete.'
      : 'Passkey sign-in did not complete.'
  const guidance: Record<string, string> = {
    NotAllowedError:
      'The browser or passkey provider did not allow the request, or it was cancelled. If no system window appeared, check your browser’s passkey settings and whether a password-manager extension is handling passkeys.',
    SecurityError:
      'The browser rejected this site’s passkey origin. Open this workspace directly in a normal browser tab using its HTTPS address.',
    NotSupportedError:
      'This browser or passkey provider does not support the requested wallet passkey. Use a provider with WebAuthn PRF support.',
    InvalidStateError:
      'The authenticator could not create this passkey. If you already saved one for this workspace, choose “Log in with passkey”.',
    AbortError: 'The passkey request was interrupted. Try again from this tab.',
    TypeError:
      'The browser or a passkey extension rejected the request. The browser detail below helps identify the compatibility issue.',
  }
  // Only include standard browser exception text, never SDK inputs, credentials,
  // PRF output or arbitrary provider objects. Keep the original cause in memory.
  const browserDetail = guidance[name] && cause instanceof Error
    ? cause.message.replace(/[\u0000-\u001f\u007f]/g, ' ').slice(0, 400)
    : ''
  const detail = `[${creating ? 'create' : 'login'} / ${name}]${browserDetail ? ` ${browserDetail}` : ''}`
  return Object.assign(
    new Error(
      `${prefix} ${guidance[name] || 'The browser returned an unexpected passkey error.'} ${detail}`,
      { cause: error },
    ),
    { code: ['NotAllowedError', 'AbortError'].includes(name) ? 4001 : 'passkey_failed' },
  )
}
