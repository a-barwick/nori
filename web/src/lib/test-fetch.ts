export type FetchHandler = (input: RequestInfo | URL, init?: RequestInit) => Response | Promise<Response>;

export function asFetch(handler: FetchHandler): typeof fetch {
  const fn = (input: RequestInfo | URL, init?: RequestInit) => Promise.resolve(handler(input, init));
  return Object.assign(fn, { preconnect: () => {} }) as typeof fetch;
}

export function mockFetch(handler: FetchHandler): () => void {
  const original = globalThis.fetch;
  globalThis.fetch = asFetch(handler);
  return () => {
    globalThis.fetch = original;
  };
}
