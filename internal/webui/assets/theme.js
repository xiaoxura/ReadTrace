(function () {
  try {
    var pref = localStorage.getItem('wxread-theme') || 'dark';
    var dark = pref === 'system'
      ? !(window.matchMedia && window.matchMedia('(prefers-color-scheme: light)').matches)
      : pref !== 'light';
    document.documentElement.dataset.theme = dark ? 'dark' : 'light';
  } catch (e) {
    document.documentElement.dataset.theme = 'dark';
  }
})();
