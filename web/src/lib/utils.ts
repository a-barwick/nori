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
