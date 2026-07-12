# Rotation, rollback, and revocation (v1)

## Provider client secret

1. Create a new provider secret and keep the current one available for rollback.
2. Render a complete new `AUTH_PROVIDERS_FILE` to a separate regular file with
   mode `0600`; preserve every provider `id` and `name`.
3. Validate the file in a canary or maintenance window, atomically replace the
   mounted file, and restart the server.
4. Complete a fresh login and verify `/user` before revoking the old secret.
5. If validation fails, restore the previous complete file and restart. Do not
   splice or partially write a live secret file.

Changing a provider UUID creates a new external identity namespace. Changing
the callback name changes the registered callback path. Neither is a secret
rotation operation.

## Server keys and sessions

Follow the deployment backup procedure before rotating token peppers or
encryption keys. Loss of the token pepper invalidates token lookups. Loss of an
encryption key can make encrypted values unrecoverable. Rotate provider access
at the identity provider to revoke new logins; revoke local sessions/tokens
through the server's administrative controls when immediate local access
removal is required.

Never log old/new secret values, session cookies, OAuth codes, state, nonce, or
PKCE verifier material during validation.
