/** Runtime operations the browser may call. The Go API serves each one at `POST /api/<name>`. */
export const RUNTIME_METHODS: readonly string[] = ["session", "status", "turn", "input", "cancel", "command", "artifact", "trace"];

export const isRuntimeMethod = (name: string) => RUNTIME_METHODS.includes(name);
