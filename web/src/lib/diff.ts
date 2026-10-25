export type DiffLine = { type: "same" | "add" | "del"; text: string };

/** Line diff via longest common subsequence. Fine for short identity docs. */
export function diffLines(before: string, after: string): DiffLine[] {
  const a = before.split("\n");
  const b = after.split("\n");
  const n = a.length;
  const m = b.length;
  const lcs: number[][] = Array.from({ length: n + 1 }, () => new Array(m + 1).fill(0));
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) {
      lcs[i][j] = a[i] === b[j] ? lcs[i + 1][j + 1] + 1 : Math.max(lcs[i + 1][j], lcs[i][j + 1]);
    }
  }
  const out: DiffLine[] = [];
  let i = 0;
  let j = 0;
  while (i < n && j < m) {
    if (a[i] === b[j]) {
      out.push({ type: "same", text: a[i] });
      i++;
      j++;
    } else if (lcs[i + 1][j] >= lcs[i][j + 1]) {
      out.push({ type: "del", text: a[i++] });
    } else {
      out.push({ type: "add", text: b[j++] });
    }
  }
  while (i < n) out.push({ type: "del", text: a[i++] });
  while (j < m) out.push({ type: "add", text: b[j++] });
  return out;
}

/** Only the changed lines plus one line of context on each side. */
export function compactDiff(lines: DiffLine[]) {
  const keep = new Set<number>();
  lines.forEach((l, i) => {
    if (l.type !== "same") [i - 1, i, i + 1].forEach((k) => k >= 0 && k < lines.length && keep.add(k));
  });
  return lines.filter((_, i) => keep.has(i));
}
