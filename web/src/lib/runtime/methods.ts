const RUNTIME_METHODS: readonly string[] = ["turn", "retry", "input", "cancel", "command", "artifact", "trace", "delivered", "seen"];

export const isRuntimeMethod = (name: string) => RUNTIME_METHODS.includes(name);

export const isRuntimeRead = (name: string) => name === "snapshot" || name === "events";
