import type { DraftFile } from "./types";

export const MAX_FILE_BYTES = 10 * 1024 * 1024;
export const MAX_FILES = 10;
export const MAX_MESSAGE_BYTES = 8192;

export const messageTooLong = (text: string) => new TextEncoder().encode(text).length > MAX_MESSAGE_BYTES;

export function hasSubmission(text: string, files: DraftFile[]) {
  return (!!text.trim() || files.length > 0) && files.every((file) => file.status === "ready");
}
