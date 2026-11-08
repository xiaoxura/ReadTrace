import { api } from './api.js';
import { icon } from './icons.js';
import { state, refreshAccounts, accountById } from './state.js';
import { runStatusBadge } from './helpers.js';
import {
  $, $$, escapeHtml, openModal, confirmDialog, toast, fmtMinutes, fmtClock, humanCron,
  copyText, debounce, setLoading, initials, lockScroll, unlockScroll,
} from './util.js';

export function avatarHTML(acc, size = 46) {
  if (acc?.avatar) {
    return `<img src="${escapeHtml(acc.avatar)}" alt="" loading="lazy">`;
  }
  return `<span style="font-size:${Math.round(size * 0.38)}px">${escapeHtml(initials(acc?.name))}</span>`;
}

export function accountSelectHTML(selected = 0, { includeAll = false, id = 'account', style = '' } = {}) {
  const opts = state.accounts.map((a) =>
    `<option value="${a.id}" ${Number(selected) === a.id ? 'selected' : ''}>${escapeHtml(a.name)}</option>`).join('');
  const all = includeAll ? `<option value="0" ${!selected ? 'selected' : ''}>全部账号</option>` : '';
  return `<select class="select" id="${id}"${style ? ` style="${escapeHtml(style)}"` : ''}>${all}${opts}</select>`;
}

const MINUTE_PRESETS = [15, 30, 60, 90, 120];

export function startRunDialog({ accountId = 0, onDone } = {}) {
  const accounts = state.accounts;
  if (!accounts.length) {
    toast('请先添加一个微信读书账号', 'warn');
    qrLoginDialog({ onDone });
    return;
  }
  const target = accountById(accountId) || accounts[0];
  const defaultMinutes = target?.default_minutes || 30;
  const { mask, close } = openModal({
    title: '开始阅读',
    subtitle: '每 30 秒上报一次阅读进度，达标后自动结束',
    body: `
      <div class="field">
        <label class="label" for="run-account">账号</label>
        ${accountSelectHTML(target.id, { id: 'run-account' })}
      </div>
      <div class="field">
        <label class="label" for="run-minutes">阅读时长 <span class="opt">分钟</span></label>
        <input class="input" id="run-minutes" type="number" min="1" max="1440" value="${escapeHtml(defaultMinutes)}">
        <div class="chips" style="margin-top:8px">
          ${MINUTE_PRESETS.map((m) => `<button type="button" class="chip ${m === defaultMinutes ? 'active' : ''}" data-min="${m}">${m} 分钟</button>`).join('')}
        </div>
        <div class="hint">账号默认时长可在「账号 → 编辑」中修改。单次最长 24 小时。</div>
      </div>
      <div class="note">
        ${icon('info')}
        <div>运行期间请保持服务在线。进度会实时同步到面板，可随时停止。</div>
      </div>`,
    footer: `<button class="btn btn-ghost" data-cancel>取消</button>
      <button class="btn btn-primary" data-start>${icon('play')}开始阅读</button>`,
  });

  $('[data-cancel]', mask).addEventListener('click', close);
  $$('.chip', mask).forEach((chip) => chip.addEventListener('click', () => {
    $$('.chip', mask).forEach((c) => c.classList.remove('active'));
    chip.classList.add('active');
    $('#run-minutes', mask).value = chip.dataset.min;
  }));
  const btn = $('[data-start]', mask);
  btn.addEventListener('click', async () => {
    const minutes = Number($('#run-minutes', mask).value) || 30;
    if (minutes < 1 || minutes > 1440) {
      toast('时长需在 1~1440 分钟之间', 'warn');
      return;
    }
    setLoading(btn, true);
    try {
      await api.startRun(Number($('#run-account', mask).value), minutes);
      close();
      toast('已开始阅读，进度实时更新中', 'ok');
      onDone?.();
      window.dispatchEvent(new CustomEvent('run:started'));
    } catch (e) {
      toast(e.message, 'error');
    } finally {
      setLoading(btn, false);
    }
  });
}

export function qrLoginDialog({ onDone } = {}) {
  let flowId = null;
  let timer = null;
  let ticker = null;
  let startedAt = 0;
  let stage = 'pending';
  let done = false;

  const { mask, close } = openModal({
    title: '扫码登录微信读书',
    subtitle: '打开手机微信「扫一扫」，建议在 1 分钟内扫码；若手机显示 4 位验证码，请在下方输入。',
    body: `
      <div class="qr-box">
        <div class="qr-frame" data-frame>
          <img data-qr alt="微信读书登录二维码">
          <div class="qr-overlay" data-overlay>
            <div>${icon('qr', 'icon')}</div>
            <div data-overlay-text>二维码已失效</div>
          </div>
        </div>
        <div class="qr-countdown" data-countdown>正在获取二维码…</div>
        <div class="qr-status" data-status><span class="pulse-dot"></span>正在获取二维码…</div>
        <div class="otp-wrap" data-otp style="width:100%;max-width:280px">
          <div class="hint" style="margin-bottom:8px">如果手机显示 4 位验证码，请在此输入；显示「登录成功」则无需填写。</div>
          <input class="input otp-input" data-otp-input maxlength="4" inputmode="numeric" placeholder="0000" aria-label="4 位数字验证码">
          <button class="btn btn-primary" data-otp-submit style="width:100%;margin-top:8px">提交验证码</button>
        </div>
        <div class="hint" style="text-align:center">如果手机显示「登录成功」，面板通常会在几秒内自动完成绑定；<br>超过 10 秒仍无反应，请刷新二维码重试。登录凭据会加密保存在本机。</div>
      </div>`,
    footer: `<button class="btn btn-ghost" data-refresh>${icon('refresh')}刷新二维码</button>
      <button class="btn" data-close>关闭</button>`,
    onClose: () => {
      stop(); // 同时清理轮询 timer 与倒计时 ticker，避免关闭后空转泄漏
      done = true;
    },
  });

  const frame = $('[data-frame]', mask);
  const img = $('[data-qr]', mask);
  const statusEl = $('[data-status]', mask);
  const countdownEl = $('[data-countdown]', mask);
  const overlayText = $('[data-overlay-text]', mask);
  const otpBox = $('[data-otp]', mask);
  const otpInput = $('[data-otp-input]', mask);

  const setStatus = (html, error = false) => {
    statusEl.innerHTML = error ? `${icon('alert')}<span>${escapeHtml(html)}</span>` : `<span class="pulse-dot"></span><span>${escapeHtml(html)}</span>`;
    statusEl.style.color = error ? 'var(--red)' : '';
  };

  const stop = () => {
    clearInterval(timer);
    clearInterval(ticker);
    timer = null;
    ticker = null;
  };

  function updateCountdown() {
    if (done) return;
    if (stage === 'need_otp') {
      countdownEl.textContent = '已扫码，请按手机提示操作';
      countdownEl.className = 'qr-countdown';
      return;
    }
    const elapsed = Math.max(0, Math.floor(Date.now() / 1000 - startedAt));
    if (elapsed < 5) {
      countdownEl.textContent = '二维码已生成，等待扫码…';
      countdownEl.className = 'qr-countdown';
      return;
    }
    countdownEl.textContent = `已等待 ${elapsed} 秒`;
    countdownEl.className = elapsed >= 120 ? 'qr-countdown urgent' : 'qr-countdown';
  }

  async function start() {
    stop();
    done = false;
    stage = 'pending';
    otpInput.value = '';
    otpBox.style.opacity = '1';
    frame.classList.remove('expired');
    img.removeAttribute('src');
    countdownEl.textContent = '正在获取二维码…';
    countdownEl.className = 'qr-countdown';
    setStatus('正在获取二维码…');
    try {
      const res = await api.qrStart();
      flowId = res.id;
      img.src = res.qr;
      startedAt = Date.now() / 1000;
      updateCountdown();
      setStatus('等待微信扫码…');
      ticker = setInterval(updateCountdown, 1000);
      timer = setInterval(check, 1200);
    } catch (e) {
      setStatus(e.message, true);
      countdownEl.textContent = '';
    }
  }

  let polling = false;

  async function check() {
    if (!flowId || done || polling) return; // 响应未返回前不重叠发起下一次轮询，避免乱序回写状态
    polling = true;
    try {
      const res = await api.qrPoll(flowId);
      switch (res.status) {
        case 'pending':
          setStatus('等待微信扫码…');
          break;
        case 'need_otp':
          stage = 'need_otp';
          otpBox.style.opacity = '1';
          updateCountdown();
          setStatus('已扫码，请输入手机上的 4 位验证码');
          otpInput.focus();
          break;
        case 'binding':
          setStatus('已扫码，正在绑定账号…');
          break;
        case 'confirmed': {
          stop();
          done = true;
          frame.classList.add('expired');
          overlayText.textContent = '登录成功';
          countdownEl.textContent = '登录成功';
          countdownEl.className = 'qr-countdown';
          const acc = res.account;
          setStatus('登录成功，正在刷新账号…');
          await refreshAccounts();
          toast(acc ? `已绑定「${acc.name}」` : '账号绑定成功', 'ok');
          close();
          onDone?.();
          break;
        }
        case 'expired':
          stop();
          frame.classList.add('expired');
          overlayText.textContent = '二维码已失效';
          countdownEl.textContent = '二维码已失效';
          countdownEl.className = 'qr-countdown dead';
          setStatus('二维码已失效，请刷新重试', true);
          break;
        case 'failed':
          stop();
          setStatus(res.error || '登录失败，请重试', true);
          break;
        default:
          break;
      }
    } catch {
      // transient network errors: keep polling
    } finally {
      polling = false;
    }
  }

  $('[data-refresh]', mask).addEventListener('click', start);
  $('[data-close]', mask).addEventListener('click', close);
  $('[data-otp-submit]', mask).addEventListener('click', async () => {
    const code = $('[data-otp-input]', mask).value.trim();
    if (!/^\d{4}$/.test(code)) {
      toast('请输入 4 位数字验证码', 'warn');
      return;
    }
    const btn = $('[data-otp-submit]', mask);
    setLoading(btn, true);
    try {
      await api.qrOtp(flowId, code);
      toast('验证成功，正在登录…', 'ok');
      stage = 'binding';
      otpInput.value = '';
      setStatus('验证成功，正在绑定账号…');
      check();
    } catch (e) {
      toast(e.message, 'error');
    } finally {
      setLoading(btn, false);
    }
  });

  start();
}

export function importAccountDialog({ onDone } = {}) {
  const { mask, close } = openModal({
    title: '导入账号',
    subtitle: '从浏览器复制一次阅读请求，或直接粘贴 Cookie',
    wide: true,
    body: `
      <div class="tabs segmented" data-tabs>
        <button class="tab active" data-tab="curl">粘贴 curl</button>
        <button class="tab" data-tab="cookie">粘贴 Cookie</button>
      </div>
      <div data-panel="curl">
        <div class="field">
          <label class="label" for="imp-curl">curl 命令</label>
          <textarea class="textarea" id="imp-curl" data-curl placeholder="curl 'https://weread.qq.com/web/book/read' -H 'cookie: ...' --data-raw '{...}'"></textarea>
          <div class="hint">在 weread.qq.com 打开任意书籍 → F12 Network → 找到 <code>read</code> 请求 → 右键 Copy as cURL (bash) → 粘贴到这里。会同时解析 Cookie 与请求体模板。</div>
        </div>
      </div>
      <div data-panel="cookie" hidden>
        <div class="field">
          <label class="label" for="imp-cookie">Cookie</label>
          <textarea class="textarea" id="imp-cookie" data-cookie placeholder="wr_vid=...; wr_skey=..."></textarea>
          <div class="hint">从浏览器开发者工具 Application → Cookies 中复制，至少需要 <code>wr_vid</code> 与 <code>wr_skey</code>。</div>
        </div>
      </div>
      <div class="grid-2">
        <div class="field">
          <label class="label" for="imp-name">账号名称 <span class="opt">可选</span></label>
          <input class="input" id="imp-name" data-name placeholder="例如：我的微信读书">
        </div>
        <div class="field">
          <label class="label">高级</label>
          <label class="check" style="height:40px"><input type="checkbox" data-force> 跳过在线校验，强制导入</label>
        </div>
      </div>
      <div class="note">${icon('shield')}<div>导入的 Cookie 会使用本机密钥加密后写入 SQLite。若勾选强制导入，账号状态会标记为「未验证」。</div></div>`,
    footer: `<button class="btn btn-ghost" data-cancel>取消</button>
      <button class="btn btn-primary" data-submit>${icon('check')}导入账号</button>`,
  });

  let tab = 'curl';
  $$('[data-tab]', mask).forEach((btn) => btn.addEventListener('click', () => {
    tab = btn.dataset.tab;
    $$('[data-tab]', mask).forEach((b) => b.classList.toggle('active', b === btn));
    $$('[data-panel]', mask).forEach((p) => { p.hidden = p.dataset.panel !== tab; });
  }));
  $('[data-cancel]', mask).addEventListener('click', close);
  const submit = $('[data-submit]', mask);
  submit.addEventListener('click', async () => {
    const payload = {
      name: $('[data-name]', mask).value.trim(),
      force: $('[data-force]', mask).checked,
    };
    if (tab === 'curl') payload.curl = $('[data-curl]', mask).value;
    else payload.cookie = $('[data-cookie]', mask).value;
    if (!payload.curl && !payload.cookie) {
      toast('请先粘贴内容', 'warn');
      return;
    }
    setLoading(submit, true);
    try {
      const res = await api.importAccount(payload);
      await refreshAccounts();
      close();
      toast(res.warning || '账号导入成功', res.warning ? 'warn' : 'ok');
      onDone?.();
    } catch (e) {
      toast(e.message, 'error');
    } finally {
      setLoading(submit, false);
    }
  });
}

export function editAccountDialog(acc, { onDone } = {}) {
  let book = {
    id: acc.book_id || '',
    title: acc.book_title || '',
    chapterId: acc.chapter_id || '',
    chapterTitle: acc.chapter_title || '',
  };
  let chapters = [];

  const channelOptions = state.channels.map((c) =>
    `<option value="${c.id}" ${acc.push_channel_id === c.id ? 'selected' : ''}>${escapeHtml(c.name)}</option>`).join('');

  const { mask, close } = openModal({
    title: `编辑「${acc.name}」`,
    subtitle: `ID #${escapeHtml(acc.seq)} · 这些设置只影响该账号`,
    wide: true,
    body: `
      <div class="grid-2">
        <div class="field">
          <label class="label" for="acc-name">账号名称</label>
          <input class="input" id="acc-name" data-name value="${escapeHtml(acc.name)}">
        </div>
        <div class="field">
          <label class="label" for="acc-minutes">默认阅读时长 <span class="opt">分钟</span></label>
          <input class="input" id="acc-minutes" type="number" min="1" max="1440" data-minutes value="${escapeHtml(acc.default_minutes)}">
        </div>
      </div>
      <div class="grid-2">
        <div class="field">
          <label class="label" for="acc-tick">心跳间隔 <span class="opt">秒</span></label>
          <select class="select" id="acc-tick" data-tick>
            ${[30, 20, 15, 10, 60].map((v) => `<option value="${v}" ${acc.tick_seconds === v ? 'selected' : ''}>${v} 秒</option>`).join('')}
          </select>
          <div class="hint">建议保持 30 秒，过短可能被服务端判定异常。</div>
        </div>
        <div class="field">
          <label class="label" for="acc-mode">阅读模式</label>
          <select class="select" id="acc-mode" data-mode>
            <option value="stable" ${acc.mode === 'stable' ? 'selected' : ''}>稳定模板（推荐）</option>
            <option value="custom" ${acc.mode === 'custom' ? 'selected' : ''}>指定书籍 + 进度推进</option>
            <option value="rotate" ${acc.mode === 'rotate' ? 'selected' : ''}>随机轮换书籍</option>
          </select>
          <div class="hint">稳定模板沿用抓包请求，成功率最高。</div>
        </div>
      </div>
      <div class="grid-2">
        <div class="field">
          <label class="label" for="acc-retries">失败自动重试</label>
          <select class="select" id="acc-retries" data-retries>
            ${[0, 1, 2, 3, 5, 8].map((v) => `<option value="${v}" ${Number(acc.max_retries) === v ? 'selected' : ''}>${v === 0 ? '不重试' : `${v} 次`}</option>`).join('')}
          </select>
          <div class="hint">阅读失败后自动重新开始本轮阅读；登录已失效的情况不会重试。</div>
        </div>
        <div class="field">
          <label class="label" for="acc-retry-delay">重试间隔 <span class="opt">秒</span></label>
          <select class="select" id="acc-retry-delay" data-retry-delay>
            ${[5, 10, 15, 30, 60, 120].map((v) => `<option value="${v}" ${Number(acc.retry_delay || 15) === v ? 'selected' : ''}>${v} 秒</option>`).join('')}
          </select>
          <div class="hint">两次重试之间的等待时间。</div>
        </div>
      </div>

      <div class="section-title">书籍与章节</div>
      <div data-book-box>
        <div class="row" style="gap:8px">
          <input class="input grow" id="acc-book-q" data-book-q placeholder="搜索书名、作者，例如：三体" aria-label="搜索书籍">
          <button class="btn btn-sm" data-book-search>${icon('search')}搜索</button>
        </div>
        <div data-book-results style="margin-top:10px"></div>
        <div data-book-current style="margin-top:10px"></div>
      </div>

      <div class="section-title">推送通知</div>
      <div class="field">
        <label class="label" for="acc-channel">完成后推送到</label>
        <select class="select" id="acc-channel" data-channel>
          <option value="0">不推送</option>
          ${channelOptions}
        </select>
        <div class="hint">可在「推送」页面添加 PushPlus / WxPusher / Telegram / ServerChan。</div>
      </div>

      <div class="section-title">高级：请求模板</div>
      <div class="note" style="margin-bottom:12px">${icon('info')}<div>模板来自浏览器真实阅读请求，包含书籍、章节与设备标识。为空时使用内置稳定模板。</div></div>
      <div class="field">
        <textarea class="textarea" id="acc-payload" data-payload placeholder="粘贴 read 请求的 curl 或 JSON 请求体（可选）" aria-label="请求模板"></textarea>
      </div>
      <div class="row">
        <button class="btn btn-sm" data-save-payload>保存模板</button>
        <button class="btn btn-sm btn-ghost" data-clear-payload>清除模板</button>
        <span class="hint" data-payload-state>${acc.has_payload ? '当前已有自定义模板' : '当前使用内置模板'}</span>
      </div>`,
    footer: `<button class="btn btn-danger" data-delete>${icon('trash')}删除账号</button>
      <span style="flex:1"></span>
      <button class="btn btn-ghost" data-cancel>取消</button>
      <button class="btn btn-primary" data-save>${icon('check')}保存</button>`,
  });

  const renderCurrent = () => {
    const box = $('[data-book-current]', mask);
    if (!book.id) {
      box.innerHTML = '';
      return;
    }
    box.innerHTML = `
      <div class="note" style="align-items:center">
        ${icon('book')}
        <div style="flex:1">
          <b>${escapeHtml(book.title || book.id)}</b>
          <div class="hint">${book.chapterTitle ? escapeHtml(book.chapterTitle) : '未指定章节，沿用模板章节'}</div>
        </div>
        <button class="btn btn-sm btn-ghost" data-clear-book>移除</button>
      </div>`;
    $('[data-clear-book]', box)?.addEventListener('click', () => {
      book = { id: '', title: '', chapterId: '', chapterTitle: '' };
      renderCurrent();
    });
  };
  renderCurrent();

  const searchBtn = $('[data-book-search]', mask);
  searchBtn.addEventListener('click', async () => {
    const q = $('[data-book-q]', mask).value.trim();
    if (!q) return;
    const results = $('[data-book-results]', mask);
    results.innerHTML = `<div class="hint">搜索中…</div>`;
    setLoading(searchBtn, true);
    try {
      const res = await api.searchBooks(acc.id, q);
      if (!res.books?.length) {
        results.innerHTML = `<div class="hint">没有找到相关书籍</div>`;
        return;
      }
      results.innerHTML = `<div class="list" style="max-height:220px;overflow:auto">${res.books.map((b, i) => `
        <div class="list-row clickable" data-book-row="${i}" style="cursor:pointer" role="button" tabindex="0" aria-label="选择 ${escapeHtml(b.title)}">
          <div class="schedule-icon" style="width:32px;height:32px">${icon('book')}</div>
          <div class="list-main">
            <div class="list-title">${escapeHtml(b.title)}</div>
            <div class="list-sub">${escapeHtml(b.author || '')} · ${escapeHtml(b.id)}</div>
          </div>
          <button class="btn btn-sm" data-pick="${i}">选择</button>
        </div>`).join('')}</div>`;
      const pickBook = async (idx) => {
        const b = res.books[idx];
        book = { id: b.id, title: b.title, chapterId: '', chapterTitle: '' };
        const modeSel = $('[data-mode]', mask);
        if (modeSel) modeSel.value = 'custom';
        renderCurrent();
        const detailEl = $('[data-book-results]', mask);
        detailEl.innerHTML = `<div class="hint">正在读取目录…</div>`;
        try {
          const detail = await api.bookDetail(acc.id, b.id);
          chapters = detail.chapters || [];
          if (!chapters.length) {
            detailEl.innerHTML = `<div class="hint">没有读取到目录，将沿用模板章节。</div>`;
            return;
          }
          const withIds = chapters.filter((c) => c.id);
          if (!withIds.length) {
            detailEl.innerHTML = `<div class="note warn">${icon('alert')}<div>该书的目录未返回可用章节 ID。你仍可指定书籍，章节将沿用模板；如需精确章节，请导入该书真实阅读请求的 curl 模板。</div></div>`;
            return;
          }
          detailEl.innerHTML = `
            <div class="field" style="margin-top:4px">
              <label class="label" for="acc-chapter">选择章节</label>
              <select class="select" id="acc-chapter" data-chapter>
                <option value="">沿用模板章节</option>
                ${withIds.map((c, i) => `<option value="${i}">${escapeHtml(c.title || `第 ${c.index} 章`)}</option>`).join('')}
              </select>
            </div>`;
          $('[data-chapter]', detailEl).addEventListener('change', (e) => {
            const idx = e.target.value;
            if (idx === '') {
              book.chapterId = '';
              book.chapterTitle = '';
            } else {
              const c = withIds[Number(idx)];
              book.chapterId = c.id;
              book.chapterTitle = c.title;
            }
            renderCurrent();
          });
        } catch (e) {
          detailEl.innerHTML = `<div class="hint">读取目录失败：${escapeHtml(e.message)}</div>`;
        }
      };
      $$('[data-pick]', results).forEach((btn) => btn.addEventListener('click', (e) => {
        e.stopPropagation();
        pickBook(Number(btn.dataset.pick));
      }));
      $$('[data-book-row]', results).forEach((row) => {
        row.addEventListener('click', (e) => {
          if (e.target.closest('[data-pick]')) return;
          pickBook(Number(row.dataset.bookRow));
        });
        row.addEventListener('keydown', (e) => {
          if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault();
            pickBook(Number(row.dataset.bookRow));
          }
        });
      });
    } catch (e) {
      results.innerHTML = `<div class="hint" style="color:var(--red)">${escapeHtml(e.message)}</div>`;
    } finally {
      setLoading(searchBtn, false);
    }
  });

  $('[data-save-payload]', mask).addEventListener('click', async (e) => {
    const val = $('[data-payload]', mask).value.trim();
    if (!val) {
      toast('请粘贴 curl 或请求体', 'warn');
      return;
    }
    setLoading(e.currentTarget, true);
    try {
      await api.savePayload(acc.id, { curl: val });
      $('[data-payload-state]', mask).textContent = '当前已有自定义模板';
      toast('模板已保存', 'ok');
    } catch (err) {
      toast(err.message, 'error');
    } finally {
      setLoading(e.currentTarget, false);
    }
  });

  $('[data-clear-payload]', mask).addEventListener('click', async () => {
    try {
      await api.clearPayload(acc.id);
      $('[data-payload-state]', mask).textContent = '当前使用内置模板';
      toast('模板已清除', 'ok');
    } catch (e) {
      toast(e.message, 'error');
    }
  });

  $('[data-cancel]', mask).addEventListener('click', close);

  $('[data-save]', mask).addEventListener('click', async (e) => {
    const name = $('[data-name]', mask).value.trim();
    if (!name) {
      toast('账号名称不能为空', 'warn');
      return;
    }
    const patch = {
      name,
      default_minutes: Number($('[data-minutes]', mask).value) || 30,
      tick_seconds: Number($('[data-tick]', mask).value) || 30,
      max_retries: Number($('[data-retries]', mask).value) || 0,
      retry_delay: Number($('[data-retry-delay]', mask).value) || 15,
      mode: $('[data-mode]', mask).value,
      push_channel_id: Number($('[data-channel]', mask).value) || 0,
      book_id: book.id,
      book_title: book.title,
      chapter_id: book.chapterId,
      chapter_title: book.chapterTitle,
    };
    setLoading(e.currentTarget, true);
    try {
      await api.updateAccount(acc.id, patch);
      await refreshAccounts();
      close();
      toast('设置已保存', 'ok');
      onDone?.();
    } catch (err) {
      toast(err.message, 'error');
    } finally {
      setLoading(e.currentTarget, false);
    }
  });

  $('[data-delete]', mask).addEventListener('click', async () => {
    const ok = await confirmDialog({
      title: '删除账号',
      message: `确定删除「${acc.name}」吗？该账号的运行记录、日志和定时任务会一并删除，且无法恢复。`,
      confirmText: '确认删除',
      danger: true,
    });
    if (!ok) return;
    try {
      await api.deleteAccount(acc.id);
      await refreshAccounts();
      close();
      toast('账号已删除', 'ok');
      onDone?.();
    } catch (e) {
      toast(e.message, 'error');
    }
  });
}

export function addScheduleDialog({ onDone, schedule = null } = {}) {
  if (!state.accounts.length) {
    toast('请先添加账号', 'warn');
    return;
  }
  const editing = !!schedule;
  const presetCron = schedule?.cron || '0 1 * * *';
  const { mask, close } = openModal({
    title: editing ? '编辑定时任务' : '新建定时任务',
    subtitle: '到点自动开始阅读，无需手动操作',
    body: `
      <div class="field">
        <label class="label" for="sch-account">账号</label>
        ${accountSelectHTML(schedule?.account_id || state.accounts[0].id, { id: 'sch-account' })}
      </div>
      <div class="field">
        <label class="label" for="sch-cron">执行时间</label>
        <div class="chips" data-presets>
          ${[
            ['0 1 * * *', '每天 01:00'], ['0 7 * * *', '每天 07:00'], ['0 12 * * *', '每天 12:00'],
            ['0 23 * * *', '每天 23:00'], ['0 */6 * * *', '每 6 小时'], ['0 */2 * * *', '每 2 小时'],
            ['0 9 * * 1-5', '工作日 09:00'],
          ].map(([c, label]) => `<button type="button" class="chip ${c === presetCron ? 'active' : ''}" data-cron="${c}">${label}</button>`).join('')}
        </div>
        <input class="input mono" id="sch-cron" data-cron-input value="${escapeHtml(presetCron)}" style="margin-top:8px" placeholder="0 1 * * *">
        <div class="hint">标准 5 段 cron：分 时 日 月 周。时区跟随「设置」中的时区。</div>
      </div>
      <div class="grid-2">
        <div class="field">
          <label class="label" for="sch-minutes">阅读时长 <span class="opt">分钟</span></label>
          <input class="input" id="sch-minutes" type="number" min="1" max="1440" data-min value="${escapeHtml(schedule?.minutes || 30)}">
        </div>
        <div class="field">
          <label class="label">启用</label>
          <label class="check" style="height:40px"><span class="switch"><input type="checkbox" data-enabled ${schedule?.enabled === false ? '' : 'checked'}><i></i></span> 保存后立即生效</label>
        </div>
      </div>
      <div class="field">
        <label class="label">接下来 3 次</label>
        <div class="list" data-preview><div class="hint">正在计算…</div></div>
      </div>`,
    footer: `<button class="btn btn-ghost" data-cancel>取消</button>
      <button class="btn btn-primary" data-save>${icon('check')}${editing ? '保存' : '创建'}</button>`,
  });

  const cronInput = $('[data-cron-input]', mask);
  const preview = $('[data-preview]', mask);
  let previewSeq = 0;

  const updatePreview = debounce(async () => {
    const cron = cronInput.value.trim();
    if (!cron) return;
    const seq = ++previewSeq;
    try {
      const res = await api.previewSchedule(cron, 3);
      if (seq !== previewSeq) return; // 慢的旧响应不覆盖新结果
      preview.innerHTML = res.times.map((t, i) => `
        <div class="list-row">
          <div class="schedule-icon" style="width:30px;height:30px">${icon('clock')}</div>
          <div class="list-main"><div class="list-title">${escapeHtml(t.text)}</div></div>
          ${i === 0 ? '<span class="badge ok">最近</span>' : ''}
        </div>`).join('');
    } catch (e) {
      if (seq !== previewSeq) return;
      preview.innerHTML = `<div class="hint" style="color:var(--red)">${escapeHtml(e.message)}</div>`;
    }
  }, 450);

  $$('[data-cron]', mask).forEach((btn) => btn.addEventListener('click', () => {
    $$('[data-cron]', mask).forEach((b) => b.classList.remove('active'));
    btn.classList.add('active');
    cronInput.value = btn.dataset.cron;
    updatePreview();
  }));
  cronInput.addEventListener('input', updatePreview);
  updatePreview();

  $('[data-cancel]', mask).addEventListener('click', close);
  const saveBtn = $('[data-save]', mask);
  saveBtn.addEventListener('click', async () => {
    const payload = {
      account_id: Number($('#sch-account', mask).value),
      cron: cronInput.value.trim(),
      minutes: Number($('[data-min]', mask).value) || 30,
      enabled: $('[data-enabled]', mask).checked,
    };
    if (!payload.cron) {
      toast('请填写 cron 表达式', 'warn');
      return;
    }
    setLoading(saveBtn, true);
    try {
      if (editing) await api.updateSchedule(schedule.id, payload);
      else await api.createSchedule(payload);
      close();
      toast(editing ? '定时任务已更新' : '定时任务已创建', 'ok');
      onDone?.();
    } catch (e) {
      toast(e.message, 'error');
    } finally {
      setLoading(saveBtn, false);
    }
  });
}

const PUSH_FIELDS = {
  pushplus: [{ key: 'token', label: 'Token', placeholder: 'PushPlus token' }],
  wxpusher: [{ key: 'spt', label: 'SPT', placeholder: 'WxPusher SPT' }],
  telegram: [
    { key: 'bot_token', label: 'Bot Token', placeholder: '123456:ABC...' },
    { key: 'chat_id', label: 'Chat ID', placeholder: '群组或用户 ID' },
    { key: 'proxy', label: '代理地址', placeholder: 'http://127.0.0.1:7890（可选）', optional: true },
  ],
  serverchan: [{ key: 'sendkey', label: 'SendKey', placeholder: 'SCT...' }],
};

// 每类事件的可自定义模板，字段为 {kind}_title / {kind}_body。
const TEMPLATE_KINDS = [
  ['success', '阅读成功', '微信读书 · 阅读完成', '账号：{{account}}\n计划：{{plan}}\n实际：{{elapsed}}\n状态：{{status}}'],
  ['failed', '阅读失败', '微信读书 · 阅读失败', '账号：{{account}}\n计划：{{plan}}\n实际：{{elapsed}}\n状态：{{status}}\n详情：{{error}}'],
  ['expired', '账号失效', '微信读书 · 账号失效', '账号：{{account}}\n状态：登录已失效，请重新扫码登录\n检查时间：{{time}}'],
];

export function pushChannelDialog({ channel = null, onDone } = {}) {
  const editing = !!channel;
  const type = channel?.type || 'pushplus';
  const { mask, close } = openModal({
    title: editing ? '编辑推送渠道' : '添加推送渠道',
    subtitle: '阅读完成后发送结果通知',
    body: `
      <div class="type-grid" data-types>
        ${[['pushplus', 'PushPlus'], ['wxpusher', 'WxPusher'], ['telegram', 'Telegram'], ['serverchan', 'ServerChan']]
          .map(([t, label]) => `<button type="button" class="type-opt ${t === type ? 'active' : ''}" data-type="${t}"><b>${label}</b>${icon(t === 'telegram' ? 'external' : 'bell')}</button>`).join('')}
      </div>
      <div class="field">
        <label class="label" for="push-name">渠道名称</label>
        <input class="input" id="push-name" data-name value="${escapeHtml(channel?.name || '')}" placeholder="例如：我的 Telegram">
      </div>
      <div data-fields></div>
      <div class="field">
        <label class="label">启用</label>
        <label class="check"><span class="switch"><input type="checkbox" data-enabled ${channel?.enabled === false ? '' : 'checked'}><i></i></span> 启用该渠道</label>
      </div>
      <div class="section-title" style="text-transform:none;letter-spacing:0">消息模板 <span style="color:var(--muted);font-size:11px;font-weight:500">可选，留空用默认文案</span></div>
      <div class="note" style="margin-bottom:12px">${icon('info')}<div>为不同事件自定义推送标题与内容（成功 / 失败 / 账号失效），留空使用默认文案。可用变量：{{account}}、{{plan}}、{{elapsed}}、{{status}}、{{error}}、{{success}}、{{failed}}、{{time}}。</div></div>
      ${TEMPLATE_KINDS.map(([k, label, defTitle, defBody]) => `
        <div class="field">
          <label class="label">${label} · 标题</label>
          <input class="input" data-tpl="${k}_title" placeholder="默认：${escapeHtml(defTitle)}"
            value="${escapeHtml(channel?.templates?.[`${k}_title`] || '')}">
          <textarea class="textarea" data-tpl="${k}_body" rows="3" style="margin-top:8px"
            placeholder="默认：${escapeHtml(defBody)}">${escapeHtml(channel?.templates?.[`${k}_body`] || '')}</textarea>
        </div>`).join('')}
      <div class="note">${icon('info')}<div>测试消息会立即发送到对应渠道，请先保存再测试。</div></div>`,
    footer: `<button class="btn btn-ghost" data-cancel>取消</button>
      <button class="btn btn-primary" data-save>${icon('check')}保存</button>`,
  });

  let currentType = type;
  const fieldsEl = $('[data-fields]', mask);

  const renderFields = () => {
    fieldsEl.innerHTML = PUSH_FIELDS[currentType].map((f) => `
      <div class="field">
        <label class="label">${f.label} ${f.optional ? '<span class="opt">可选</span>' : ''}</label>
        <input class="input" data-key="${f.key}" placeholder="${f.placeholder}"
          value="${escapeHtml(channel?.config?.[f.key] || '')}">
      </div>`).join('');
  };
  renderFields();

  $$('[data-type]', mask).forEach((btn) => btn.addEventListener('click', () => {
    $$('[data-type]', mask).forEach((b) => b.classList.remove('active'));
    btn.classList.add('active');
    currentType = btn.dataset.type;
    renderFields();
  }));

  $('[data-cancel]', mask).addEventListener('click', close);
  const saveBtn = $('[data-save]', mask);
  saveBtn.addEventListener('click', async () => {
    const config = {};
    $$('[data-key]', mask).forEach((input) => { config[input.dataset.key] = input.value.trim(); });
    const templates = {};
    $$('[data-tpl]', mask).forEach((el) => { templates[el.dataset.tpl] = el.value.trim(); });
    const payload = {
      name: $('[data-name]', mask).value.trim(),
      type: currentType,
      config,
      templates,
      enabled: $('[data-enabled]', mask).checked,
    };
    setLoading(saveBtn, true);
    try {
      if (editing) await api.updatePush(channel.id, payload);
      else await api.createPush(payload);
      close();
      toast(editing ? '渠道已更新' : '渠道已添加', 'ok');
      onDone?.();
    } catch (e) {
      toast(e.message, 'error');
    } finally {
      setLoading(saveBtn, false);
    }
  });
}

export function runDetailDrawer(runId, { onDone } = {}) {
  const mask = document.createElement('div');
  mask.className = 'drawer-mask';
  const drawer = document.createElement('div');
  drawer.className = 'drawer';
  drawer.setAttribute('role', 'dialog');
  drawer.setAttribute('aria-modal', 'true');
  drawer.setAttribute('aria-label', '运行详情');
  drawer.innerHTML = `
    <div class="drawer-head">
      <div>
        <div class="modal-title" data-title>运行详情</div>
        <div class="modal-sub" data-sub>加载中…</div>
      </div>
      <button class="modal-close" data-close aria-label="关闭">${icon('close')}</button>
    </div>
    <div class="drawer-body">
      <div class="grid-2" data-stats style="margin-bottom:14px"></div>
      <div class="card-title" style="margin-bottom:10px">${icon('list')}实时日志</div>
      <div class="console" data-console><div class="empty-log">加载日志…</div></div>
    </div>
    <div class="drawer-foot">
      <button class="btn btn-sm btn-ghost" data-copy>${icon('copy')}复制日志</button>
      <button class="btn btn-sm btn-danger" data-stop>${icon('stop')}停止运行</button>
      <button class="btn btn-sm" data-close2>关闭</button>
    </div>`;
  document.body.appendChild(mask);
  document.body.appendChild(drawer);
  lockScroll();

  let closed = false;
  let logAfter = 0;
  let baseRun = null;
  const consoleEl = $('[data-console]', drawer);
  const stopBtn = $('[data-stop]', drawer);

  const onKey = (e) => {
    if (e.key !== 'Escape') return;
    // 上层若还有 modal（如确认框），Escape 交给它处理
    if (document.querySelector('.modal-mask')) return;
    close();
  };

  const close = () => {
    if (closed) return;
    closed = true;
    mask.remove();
    drawer.remove();
    unlockScroll();
    document.removeEventListener('keydown', onKey);
    window.removeEventListener('app:event', onEvent);
    onDone?.();
  };
  mask.addEventListener('click', close);
  $('[data-close]', drawer).addEventListener('click', close);
  $('[data-close2]', drawer).addEventListener('click', close);
  document.addEventListener('keydown', onKey);
  $('[data-close]', drawer).focus();

  const appendLogs = (logs) => {
    if (!logs?.length) return;
    if (consoleEl.querySelector('.empty-log')) consoleEl.innerHTML = '';
    for (const log of logs) {
      if (log.id && log.id <= logAfter && baseRun) continue; // SSE 与初次加载的边界重复行去重
      logAfter = Math.max(logAfter, log.id || 0);
      const line = document.createElement('div');
      line.className = `log-line ${escapeHtml(log.level)}`;
      line.innerHTML = `<span class="t">${new Date(log.ts * 1000).toLocaleTimeString('zh-CN', { hour12: false })}</span><span class="lv">${escapeHtml(String(log.level).toUpperCase())}</span><span class="m">${escapeHtml(log.message)}</span>`;
      consoleEl.appendChild(line);
    }
    consoleEl.scrollTop = consoleEl.scrollHeight;
  };

  const paintRun = (run) => {
    $('[data-title]', drawer).textContent = `运行 #${run.id}`;
    $('[data-sub]', drawer).textContent = `${run.account_name || ''} · ${run.trigger === 'schedule' ? '定时任务' : '手动开始'} · ${fmtClock(run.started_at, state.tz)}`;
    const pct = run.plan_seconds ? Math.min(100, Math.round((run.elapsed_seconds / run.plan_seconds) * 100)) : 0;
    $('[data-stats]', drawer).innerHTML = `
      <div class="card" style="padding:14px">
        <div class="stat-label">进度</div>
        <div class="stat-value" style="font-size:22px">${fmtMinutes(run.elapsed_seconds)}<small>/ ${fmtMinutes(run.plan_seconds)} 分</small></div>
        <div class="bar" style="margin-top:10px"><i style="width:${pct}%"></i></div>
      </div>
      <div class="card" style="padding:14px">
        <div class="stat-label">状态</div>
        <div style="margin-top:8px">${runStatusBadge(run.status, '阅读中')}</div>
        <div class="hint" style="margin-top:8px">成功 ${run.success_ticks} 次 · 失败 ${run.failed_ticks} 次</div>
      </div>`;
    stopBtn.style.display = run.status === 'running' ? '' : 'none';
  };

  const onEvent = (e) => {
    const ev = e.detail;
    if (ev.run_id !== runId) return;
    if (ev.type === 'run.log' && ev.data) appendLogs([ev.data]);
    if (ev.type === 'run.tick' && ev.data) {
      const d = ev.data;
      if (!baseRun) {
        baseRun = {
          id: runId, account_name: d.account_name, trigger: 'manual',
          started_at: d.started_at, elapsed_seconds: 0, plan_seconds: d.target || 0,
          success_ticks: 0, failed_ticks: 0, status: 'running',
        };
      }
      baseRun.elapsed_seconds = d.elapsed;
      baseRun.plan_seconds = d.target;
      baseRun.success_ticks = d.success;
      baseRun.failed_ticks = d.failed;
      baseRun.status = 'running';
      paintRun(baseRun);
    }
    if (ev.type === 'run.end') {
      stopBtn.style.display = 'none';
      if (baseRun) baseRun.status = ev.data?.status || 'success';
      if (baseRun) paintRun(baseRun);
      api.run(runId).then((res) => {
        if (closed || !res?.run) return;
        baseRun = res.run;
        paintRun(res.run);
      }).catch(() => { /* 忽略结账查询失败，界面已有 end 状态 */ });
      toast(`运行 #${runId} 已结束`, 'info');
    }
  };
  window.addEventListener('app:event', onEvent);

  stopBtn.addEventListener('click', async () => {
    const ok = await confirmDialog({ title: '停止运行', message: '确定停止本次阅读吗？已完成的时长会保留。', confirmText: '停止', danger: true });
    if (!ok) return;
    try {
      await api.stopRun(runId);
      toast('已停止', 'ok');
    } catch (e) {
      toast(e.message, 'error');
    }
  });

  $('[data-copy]', drawer).addEventListener('click', () => {
    const text = Array.from(consoleEl.querySelectorAll('.log-line')).map((l) => l.textContent).join('\n');
    copyText(text);
  });

  (async () => {
    try {
      const res = await api.run(runId);
      if (closed) return;
      baseRun = res.run;
      paintRun(res.run);
      appendLogs(res.logs);
    } catch (e) {
      toast(e.message, 'error');
      close();
    }
  })();
}
