// content script の fetch はページの CORS に縛られるため、ここで中継する。
const SERVER = 'http://127.0.0.1:8787';

chrome.runtime.onMessage.addListener(({ path, method, body }, _sender, sendResponse) => {
  fetch(SERVER + path, {
    method,
    headers: { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
    .then(async (res) => sendResponse({ ok: res.ok, data: res.ok ? await res.json() : null }))
    .catch((err) => sendResponse({ ok: false, error: String(err) }));
  return true;
});
