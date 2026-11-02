/** Runtime operations the browser may call. The Go API serves each one at `POST /api/<name>`. */
export const RUNTIME_METHODS: readonly string[] = ["turn", "input", "cancel", "command", "artifact", "trace"];

export const isRuntimeMethod = (name: string) => RUNTIME_METHODS.includes(name);

/** Reads the browser may make. The Go API serves each one at `GET /v1/<name>`. */
export const isRuntimeRead = (name: string) => name === "snapshot" || name === "events";
