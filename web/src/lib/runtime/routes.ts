import manifest from "../contracts/routes.generated.json";

type Rule = {
  path: string;
  method: string;
  callers: string[];
  body?: string;
  max_request_bytes?: number;
  response_media_types: string[];
  path_parameters?: Record<string, string>;
  max_path_segments?: number;
};

const rules = (manifest.routes as Rule[]).map(rule => {
  const segments = rule.path.slice(1).split("/").map(segment => {
    if (!segment.startsWith("{")) return { literal: segment };
    const name = segment.slice(1, -1);
    const rest = name.endsWith("...");
    return { pattern: new RegExp(rule.path_parameters![rest ? name.slice(0, -3) : name]), rest };
  });
  return { rule, segments, specificity: segments.reduce((total, segment) => total + ("literal" in segment ? 2 : segment.rest ? 0 : 1), 0) };
}).sort((a, b) => b.specificity - a.specificity);

export function browserRoute(method: string, segments: string[]): Rule | undefined {
  if (segments.some(segment => !segment || segment === "." || segment === ".." || /[/\\\0]/.test(segment))) return;
  const candidates = rules.filter(({ rule, segments: pattern }) => {
    if (rule.method !== method && !(method === "HEAD" && rule.method === "GET") && rule.method !== "*") return false;
    const tail = pattern.at(-1);
    if (tail?.rest ? segments.length < pattern.length : segments.length !== pattern.length) return false;
    return pattern.every((part, index) => part.literal === undefined || part.literal === segments[index]);
  });
  const match = candidates[0];
  if (!match?.rule.callers.includes("browser")) return;
  if (match.rule.max_path_segments && segments.length > match.rule.max_path_segments) return;
  const valid = match.segments.every((part, index) => {
    const expression = part.pattern;
    if (!expression) return true;
    const values = part.rest ? segments.slice(index) : [segments[index]];
    return values.every(value => expression.test(value));
  });
  return valid ? match.rule : undefined;
}
