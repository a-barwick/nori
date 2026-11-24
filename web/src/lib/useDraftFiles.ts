"use client";

import { useCallback, useRef, useState } from "react";
import { MAX_FILE_BYTES, MAX_FILES } from "./submission";
import type { Attachment, DraftFile } from "./types";

export type UploadContext = { signal: AbortSignal; progress: (fraction: number) => void };
type Upload = (item: DraftFile, context: UploadContext) => Promise<Attachment>;

export function useDraftFiles() {
  const [files, setFiles] = useState<DraftFile[]>([]);
  const current = useRef<DraftFile[]>([]);
  const generation = useRef(0);
  const uploads = useRef(new Map<string, AbortController>());
  const update = useCallback((transform: (files: DraftFile[]) => DraftFile[]) => {
    current.current = transform(current.current);
    setFiles(current.current);
  }, []);
  const change = useCallback((id: string, changes: Partial<DraftFile>) => {
    update((rows) => rows.map((item) => item.id === id ? { ...item, ...changes } : item));
  }, [update]);
  const clear = useCallback(() => {
    generation.current++;
    update(() => []);
  }, [update]);
  const invalidateUploads = useCallback(() => {
    generation.current++;
    update((rows) => rows.map((item) => item.size > MAX_FILE_BYTES ? item : {
      ...item, status: "error", error: "The upload could not be confirmed. Retry this file.",
    }));
  }, [update]);
  // Removing a file that is still uploading cancels its upload.
  const remove = (id: string) => { uploads.current.get(id)?.abort(); uploads.current.delete(id); update((rows) => rows.filter((item) => item.id !== id)); };

  const save = async (item: DraftFile, upload: Upload) => {
    const startedIn = generation.current;
    const controller = new AbortController();
    uploads.current.set(item.id, controller);
    try {
      const saved = await upload(item, { signal: controller.signal, progress: (fraction) => change(item.id, { progress: fraction }) });
      if (generation.current === startedIn) change(item.id, { ...saved, status: "ready", progress: undefined });
    } catch (error) {
      if (controller.signal.aborted) return;
      if (generation.current === startedIn) {
        change(item.id, { status: "error", progress: undefined, error: error instanceof Error ? error.message : "Upload failed. Try again." });
      }
    } finally {
      if (uploads.current.get(item.id) === controller) uploads.current.delete(item.id);
    }
  };

  function attach(picked: File[], upload: Upload, onLimit: (message: string) => void) {
    const room = MAX_FILES - current.current.length;
    if (picked.length > room) onLimit(`A message can have at most ${MAX_FILES} files.`);
    const items = picked.slice(0, room).map((file): DraftFile => ({
      id: crypto.randomUUID(), name: file.name, media_type: file.type, size: file.size, file,
      ...(file.size > MAX_FILE_BYTES ? { status: "error", error: "A file can be at most 10 MB." } : { status: "uploading" }),
    }));
    update((rows) => [...rows, ...items]);
    for (const item of items) if (item.status === "uploading") void save(item, upload);
  }

  function retry(item: DraftFile, upload: Upload) {
    if (item.size > MAX_FILE_BYTES || !current.current.some((file) => file.id === item.id && file.status === "error")) return;
    // Reuse the upload ID if the service stored the file before its response was lost.
    change(item.id, { status: "uploading", error: undefined });
    void save(item, upload);
  }

  return { files, attach, retry, clear, remove, invalidateUploads };
}
