export { clsx as cn } from "clsx";

export function firstSentence(text: string) {
  const plain = text
    .replace(/<!--[\s\S]*?-->/g, "")
    .replace(/[#*_`>|\[\]]/g, "")
    .replace(/\(https?:[^)]*\)/g, "")
    .trim();
  const m = plain.match(/^[\s\S]*?[.!?](\s|$)/);
  return (m ? m[0] : plain).trim().slice(0, 180);
}

function cleanTitle(raw: string) {
  let s = raw.trim().replace(/^[*_`]+|[*_`]+$/g, "").trim();
  if ((s.startsWith('"') && s.endsWith('"')) || (s.startsWith("'") && s.endsWith("'")) || (s.startsWith("“") && s.endsWith("”"))) {
    s = s.slice(1, -1).trim();
  }
  return s;
}

export function extractDocumentTitle(body: string): { title?: string; rest: string } {
  const lines = body.replace(/\r\n/g, "\n").split("\n");
  let checked = 0;
  for (let i = 0; i < lines.length; i += 1) {
    const line = lines[i].trim();
    if (!line) continue;
    checked += 1;
    if (checked > 4) break;
    const heading = line.match(/^#{1,3}\s+(.+)$/);
    const titled = line.match(/^(?:\*\*)?title(?:\*\*)?:\s*(.+)$/i);
    const boldOnly = checked === 1 && line.length <= 140 ? line.match(/^\*\*([^*]+)\*\*$/) : null;
    const raw = heading?.[1] ?? titled?.[1] ?? boldOnly?.[1];
    if (raw) {
      const title = cleanTitle(raw);
      if (title) {
        return { title, rest: lines.slice(i + 1).join("\n").trim() };
      }
    }
    if (checked === 1) break;
  }
  return { rest: body.trim() };
}

export function documentTitle(body: string) {
  return extractDocumentTitle(body).title ?? (firstSentence(body).slice(0, 64) || "Document");
}

export function resolveDocumentPresentation(
  body: string,
  explicitTitle?: string,
): { title: string; body: string } {
  const extracted = extractDocumentTitle(body);
  const trimmedExplicit = explicitTitle?.trim();
  if (trimmedExplicit) {
    const matchesExtracted =
      !!extracted.title &&
      extracted.title.trim().toLowerCase() === trimmedExplicit.toLowerCase();
    return {
      title: trimmedExplicit,
      body: matchesExtracted ? extracted.rest : body,
    };
  }
  const title = extracted.title || firstSentence(body).slice(0, 64) || "Document";
  return {
    title,
    body: extracted.title ? extracted.rest : body,
  };
}
