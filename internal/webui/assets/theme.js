(function () {
  var mq = window.matchMedia ? window.matchMedia('(prefers-color-scheme: light)') : null;
  var pref = 'dark';
  try {
    pref = localStorage.getItem('wxread-theme') || 'dark';
  } catch (e) { /* 私密模式下 localStorage 不可用 */ }

  function resolve() {
    var dark = pref === 'system' ? !(mq && mq.matches) : pref !== 'light';
    var theme = dark ? 'dark' : 'light';
    document.documentElement.dataset.theme = theme;
    var meta = document.querySelector('meta[name="theme-color"]');
    if (meta) meta.setAttribute('content', dark ? '#070a0f' : '#f5f3ee');
    return theme;
  }

  resolve();

  try {
    if (mq && typeof mq.addEventListener === 'function') {
      mq.addEventListener('change', resolve);
    } else if (mq && typeof mq.addListener === 'function') {
      mq.addListener(resolve);
    }
  } catch (e) { /* 旧浏览器忽略 */ }
})();
