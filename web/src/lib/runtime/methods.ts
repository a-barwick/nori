import manifest from "../contracts/routes.generated.json";

const operations = new Set(manifest.routes.flatMap(route =>
  route.method === "POST" && route.path.startsWith("/api/") && route.callers.includes("browser")
    ? [route.path.slice("/api/".length)] : [],
));

export const isRuntimeMethod = (name: string) => operations.has(name);
