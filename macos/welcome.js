"use strict";

const button = document.querySelector("button");
const message = document.querySelector("p");
function show(status) {
  button.textContent = status.label;
  button.disabled = status.busy || status.unconfigured;
  message.textContent = status.message;
  message.dataset.error = String(status.error);
}
window.noriWelcome.subscribe(show);
window.noriWelcome.status().then(show).catch(() => {
  show({ label: "Sign in with Google", unconfigured: true, error: true, message: "Nori couldn’t open sign-in." });
});
button.addEventListener("click", async () => {
  button.disabled = true;
  try {
    await window.noriWelcome.signIn();
  } catch {
    show({ label: "Sign in with Google", error: true, message: "Nori couldn’t complete sign-in. Try again." });
  }
});
