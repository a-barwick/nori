/**
 * The page a browser gets when it asks for a screen while Nori's service is unreachable or busy. It says so once and
 * checks again with a growing wait (2 s, 4 s, … up to 30 s), then opens the screen a single time when the service
 * answers. The wait keeps growing across those openings (sessionStorage), so a service that flaps can't cause a
 * reload storm. It is self-contained: no app scripts, styles or fonts, which may be what is unavailable.
 */
export function reconnectingPage(status: 429 | 503, retryAfter?: string) {
  const busy = status === 429;
  const first = Math.min(30, Math.max(2, Number(retryAfter) || 2));
  const html = `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="robots" content="noindex"><title>Nori · reconnecting</title><style>
:root{color-scheme:light dark;--bg:#faf8fd;--card:#fff;--ink:#1d1630;--ink2:#625a73;--line:#e7e2ef;--accent:#5000ff}
@media (prefers-color-scheme:dark){:root{--bg:#121019;--card:#1b1825;--ink:#f1eef8;--ink2:#a8a1b8;--line:#2e2a3a;--accent:#9b7bff}}
body{margin:0;min-height:100vh;display:grid;place-items:center;background:var(--bg);color:var(--ink);font:15px/1.45 system-ui,-apple-system,sans-serif;padding:16px;box-sizing:border-box}
main{max-width:22rem;background:var(--card);border:1px solid var(--line);border-radius:16px;padding:20px;text-align:center}
h1{font-size:16px;margin:0 0 4px}p{margin:0;color:var(--ink2)}
.dot{display:inline-block;width:8px;height:8px;border-radius:50%;background:var(--accent);margin-right:8px;animation:p 1.6s ease-in-out infinite}
@keyframes p{50%{opacity:.25}}@media (prefers-reduced-motion:reduce){.dot{animation:none}}
button{margin-top:14px;font:inherit;font-weight:500;border:1px solid var(--line);background:none;color:var(--ink);border-radius:999px;padding:6px 16px;cursor:pointer}
</style></head><body><main role="status" aria-live="polite">
<h1><span class="dot" aria-hidden="true"></span>${busy ? "Nori is busy" : "Reconnecting to Nori"}</h1>
<p id="note">${busy ? "Nori has a lot to do right now." : "Nori's service isn't answering right now."} This page opens by itself when it is back.</p>
<button type="button" id="now">Try now</button></main><script>
(function(){
  var key="nori-reconnect", attempt=0, timer, checking=false;
  try{var s=JSON.parse(sessionStorage.getItem(key)||"null");if(s&&Date.now()-s.at<120000)attempt=s.attempt}catch(e){}
  function save(){try{sessionStorage.setItem(key,JSON.stringify({attempt:attempt,at:Date.now()}))}catch(e){}}
  function wait(){return Math.min(30,${first}*Math.pow(2,attempt))*1000}
  function check(){
    if(checking)return;checking=true;clearTimeout(timer);
    fetch(location.href,{method:"HEAD",cache:"no-store",credentials:"same-origin"}).then(function(r){
      if(r.ok){attempt++;save();location.reload();return}
      checking=false;attempt++;save();schedule();
    },function(){checking=false;attempt++;save();schedule()});
  }
  function schedule(){clearTimeout(timer);if(document.visibilityState==="visible")timer=setTimeout(check,wait())}
  document.getElementById("now").onclick=function(){attempt=0;save();check()};
  document.addEventListener("visibilitychange",schedule);
  addEventListener("online",function(){attempt=0;check()});
  schedule();
})();
</script></body></html>`;
  const headers = new Headers({ "Content-Type": "text/html; charset=utf-8", "Cache-Control": "no-store" });
  if (retryAfter) headers.set("Retry-After", retryAfter);
  return new Response(html, { status, headers });
}
