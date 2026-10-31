const INTERVAL_MS = 5000;
const LIST_PATH = '/contestant/benchmark_jobs';
const DETAIL_PATH = /^\/contestant\/benchmark_jobs\/\d+$/;

const api = (method, path, body) => chrome.runtime.sendMessage({ method, path, body });

// CSRF トークンを送らないよう、head を含むページ全体は送らない。
const report = (element) => api('POST', '/html', { path: location.pathname, html: element.outerHTML });

const firstPending = async () => (await api('GET', '/enqueues')).data?.find((e) => e.status === 'pending');

async function tickList(main) {
  // 絞り込み中は実行中のジョブが表から隠れるので、判定材料にしない。
  if (!main.querySelector('table') || main.querySelector('select[name=status]')?.value) return;

  const sent = await report(main);
  if (!sent.ok) return;

  const button = main.querySelector('form button[type=submit].is-primary');
  if (!button || button.disabled) return;

  const pending = await firstPending();
  if (!pending) return;

  const claim = await api('POST', `/enqueues/${pending.id}/claim`, {});
  if (claim.ok) button.click();
}

// 実行中のジョブを見ていた詳細ページ。過去のジョブを読んでいる人を一覧へ飛ばさないために覚える。
let activePath = null;

async function tickDetail() {
  // Conclusion が main の外の aside に出ても保存できるよう、#app ごと送る。
  const sent = await report(document.querySelector('#app'));
  if (!sent.ok || sent.data.jobs.length === 0) return;
  if (sent.data.active) {
    activePath = location.pathname;
    return;
  }
  // 次の要求は一覧でしか押せないので、待っている要求があれば一覧へ戻る。
  if (activePath === location.pathname || (await firstPending())) location.assign(LIST_PATH);
}

async function tick() {
  const main = document.querySelector('main');
  if (!main) return;
  if (location.pathname === LIST_PATH) await tickList(main);
  else if (DETAIL_PATH.test(location.pathname)) await tickDetail();
}

let running = false;
setInterval(async () => {
  if (running) return;
  running = true;
  try {
    await tick();
  } catch (err) {
    console.warn('bench-enqueue:', err);
  } finally {
    running = false;
  }
}, INTERVAL_MS);
