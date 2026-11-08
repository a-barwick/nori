export function runtimePath(agentMode: boolean, path: string) {
  return `${agentMode ? "/api/agent" : "/api"}/${path}`;
}
