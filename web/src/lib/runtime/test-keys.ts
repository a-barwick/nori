import { exportJWK, generateKeyPair } from "jose";

let keypairPromise: Promise<{ privateKey: CryptoKey; jwks: { keys: Record<string, unknown>[] } }> | undefined;
export function testIdentityKeys() {
  keypairPromise ??= (async () => {
    const { privateKey, publicKey } = await generateKeyPair("ES256");
    const jwk = await exportJWK(publicKey);
    return { privateKey, jwks: { keys: [{ ...jwk, kid: "auth-k1", alg: "ES256", use: "sig" }] } };
  })();
  return keypairPromise;
}
