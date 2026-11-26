export function announce(message: string) {
  if (typeof document === "undefined") return;
  const region = document.getElementById("nori-announcer");
  if (!region) return;
  region.textContent = "";
  requestAnimationFrame(() => { region.textContent = message; });
}
