type ReadResult<T> = { ok: true; value: T } | { ok: false; status: 400 | 413 };

export async function readBytesLimited(request: Request, limit: number): Promise<ReadResult<ArrayBuffer>> {
  const length = Number(request.headers.get("content-length") ?? 0);
  if (Number.isFinite(length) && length > limit) {
    await request.body?.cancel().catch(() => undefined);
    return { ok: false, status: 413 };
  }
  if (!request.body) return { ok: true, value: new ArrayBuffer(0) };
  const reader = request.body.getReader();
  const chunks: Uint8Array[] = [];
  let total = 0;
  try {
    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      if (!value) continue;
      total += value.byteLength;
      if (total > limit) {
        await reader.cancel().catch(() => undefined);
        return { ok: false, status: 413 };
      }
      chunks.push(value);
    }
  } catch {
    await reader.cancel().catch(() => undefined);
    return { ok: false, status: 400 };
  } finally {
    reader.releaseLock();
  }
  const buffer = new ArrayBuffer(total);
  const out = new Uint8Array(buffer);
  let offset = 0;
  for (const chunk of chunks) {
    out.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return { ok: true, value: buffer };
}

export async function readTextLimited(request: Request, limit: number): Promise<ReadResult<string>> {
  const bytes = await readBytesLimited(request, limit);
  if (!bytes.ok) return bytes;
  try {
    return { ok: true, value: new TextDecoder("utf-8", { fatal: true }).decode(bytes.value) };
  } catch {
    return { ok: false, status: 400 };
  }
}
