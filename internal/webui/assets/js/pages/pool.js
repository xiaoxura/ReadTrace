import { api } from '../api.js';
import { state, refreshAccounts } from '../state.js';
import { icon } from '../icons.js';
import {
  $, $$, escapeHtml, toast, setLoading, confirmDialog, openModal, pageHeaderHTML,
} from '../util.js';
import { skeletonHTML, pagerHTML, bindPager, pageSlice } from '../helpers.js';

const POOL_TABS = [
  ['import', '书籍导入', 'download'],
  ['manage', '书池管理', 'book'],
];
let poolTab = 'import';

let manageAccount = 0;    // 书池管理当前所属账号（0=未选，展示时回退第一个账号）
let shelfCache = null;    // 最近一次书架审查数据（books / audit / in_pool）
let poolCache = null;     // 最近一次书池数据
let poolPage = 1;         // 书池列表分页状态
let poolPageSize = 20;
let shelfPage = 1;        // 书架审查列表分页状态
let shelfPageSize = 20;
let importEpoch = 0;      // 切换账号或重新查询后忽略旧响应
let poolRequestEpoch = 0; // 多次刷新书池时只展示最后一次结果

export async function renderPool(ctx) {
  const { root } = ctx;
  root.innerHTML = skeletonHTML(['height:44px;width:40%', 'height:280px']);

  if (!state.accounts.length) {
    try { await refreshAccounts(); } catch { /* 书池页不依赖账号列表也应可用 */ }
  }
  if (!ctx.isActive()) return;

  root.innerHTML = `
    ${pageHeaderHTML({ title: '书池管理', sub: '阅读的书籍来源 · 按账号独立 · 指纹模板在设置页' })}
    <div class="card" style="margin-bottom:16px;padding:10px 14px">
      <nav class="pool-tabs" data-pool-tabs aria-label="书池子菜单">
        ${POOL_TABS.map(([key, label, ic]) => `
          <button class="pool-tab" data-pool-tab="${key}">${icon(ic)}<span>${label}</span></button>
        `).join('')}
      </nav>
    </div>
    <div data-pool-content></div>
  `;

  const content = $('[data-pool-content]');
  const renderTab = () => {
    if (poolTab === 'import') return drawImportTab(content);
    return drawManageTab(content);
  };
  $$('[data-pool-tab]').forEach((btn) => btn.addEventListener('click', () => {
    poolTab = btn.dataset.poolTab;
    $$('[data-pool-tab]').forEach((b) => b.classList.toggle('active', b === btn));
    window.scrollTo({ top: 0, behavior: 'instant' });
    renderTab();
  }));
  if (!POOL_TABS.some(([k]) => k === poolTab)) poolTab = 'import';
  $(`[data-pool-tab="${poolTab}"]`).classList.add('active');
  renderTab();
}

/* ---------- 账号选择器（书籍导入与书池管理共用） ---------- */

function activeAccounts() {
  return state.accounts.filter((a) => a.status === 'active');
}

function accountLabel(a) {
  return a.name || a.nickname || `账号 #${a.seq ?? '?'}`;
}

/* ---------- 二级菜单一：书籍导入（一键书架 + ID/书名查询） ---------- */

function drawImportTab(box) {
  importEpoch++;
  shelfCache = null;
  shelfPage = 1;
  const accounts = activeAccounts();
  if (!accounts.length) {
    box.innerHTML = `
      <article class="card">
        <div class="card-head"><div><div class="card-title">${icon('download')}书籍导入</div><div class="card-sub">一键导入书架，或按书籍 ID / 书名查询导入</div></div></div>
        <div class="note warn">${icon('alert')}<div>还没有登录可用的微信读书账号，无法拉取书架和查询书籍。可先到「账号」页扫码绑定。</div></div>
      </article>
    `;
    return;
  }
  box.innerHTML = `
    <article class="card">
      <div class="card-head"><div><div class="card-title">${icon('download')}书籍导入</div><div class="card-sub">一键从书架导入全部可读书籍；未付费的书籍会自动跳过</div></div></div>
      <div class="tool-row" style="margin-bottom:12px">
        <select class="select" id="shelf-account" aria-label="书籍导入账号" style="min-width:200px">
          ${accounts.map((a) => `<option value="${a.id}">${escapeHtml(accountLabel(a))}</option>`).join('')}
        </select>
        <label style="display:flex;align-items:center;gap:6px;font-size:12px;cursor:pointer">
          <input type="checkbox" id="shelf-check-chapters" checked> 同时导入章节
        </label>
        <label style="display:flex;align-items:center;gap:6px;font-size:12px;cursor:pointer" title="已购买全本或使用无限卡/会员的账号，官方目录仍会把章节标成付费，勾选后不再过滤，全部导入。未购买的书请勿勾选。">
          <input type="checkbox" id="shelf-ignore-paid"> 全本导入（已购/会员）
        </label>
        <select class="select" id="shelf-chapters-per" style="width:170px">
          <option value="0" selected>全部可读章节</option>
          <option value="20">每本前 20 章</option>
          <option value="50">每本前 50 章</option>
          <option value="100">每本前 100 章</option>
        </select>
        <span style="flex:1"></span>
        <button class="btn btn-primary" id="shelf-load">${icon('download')}拉取书架</button>
      </div>
      <div class="section-title" style="margin-top:4px">按 ID / 书名查询导入</div>
      <div class="tool-row">
        <input class="input" id="book-query" placeholder="输入书籍 ID 或 书名，例如：平凡的世界" style="min-width:260px">
        <button class="btn" id="book-query-btn">${icon('search')}查询</button>
      </div>
      <div data-import-result><div class="hint">查询书籍后在这里显示结果，逐本点「导入」写入书池。</div></div>
    </article>
  `;
  $('#book-query', box).addEventListener('keydown', (e) => {
    if (e.key === 'Enter') $('#book-query-btn', box).click();
  });
  $('#book-query-btn', box).addEventListener('click', () => queryBooks());
  $('#shelf-load', box).addEventListener('click', () => loadShelf());
  $('#shelf-account', box).addEventListener('change', () => {
    importEpoch++;
    shelfCache = null;
    shelfPage = 1;
    setLoading($('#book-query-btn', box), false);
    setLoading($('#shelf-load', box), false);
    $('[data-import-result]', box).innerHTML = '<div class="hint">已切换账号，请重新查询书籍或拉取书架。</div>';
  });
}

function currentImport(accountId, epoch, host) {
  return poolTab === 'import' && host?.isConnected && epoch === importEpoch
    && Number($('#shelf-account', $('#page'))?.value || 0) === accountId;
}

async function queryBooks() {
  const box = $('#page');
  const q = $('#book-query', box).value.trim();
  const host = $('[data-import-result]', box);
  if (!q) { toast('请输入书籍 ID 或书名', 'warn'); return; }
  const accountId = Number($('#shelf-account', box).value || 0);
  const epoch = ++importEpoch;
  shelfCache = null;
  const btn = $('#book-query-btn', box);
  setLoading($('#shelf-load', box), false);
  setLoading(btn, true);
  host.innerHTML = `<div class="hint">查询中…</div>`;
  const single = /^[0-9]{4,}$/.test(q);
  try {
    if (single) {
      // 直接按 bookId 拉详情与目录；失败再退回按书名搜索一次。
      try {
        const detail = await api.bookDetail(accountId, q);
        if (!currentImport(accountId, epoch, host)) return;
        if (detail?.book) {
          drawBookCandidates([detail.book], auditFromChapters(detail.chapters || []), accountId, epoch, host);
          return;
        }
      } catch (e) {
        // fallthrough to keyword search
        if (!currentImport(accountId, epoch, host)) return;
      }
    }
    const res = await api.searchBooks(accountId, q);
    if (!currentImport(accountId, epoch, host)) return;
    if (!res.books?.length) {
      host.innerHTML = `<div class="empty" style="padding:22px"><h3>没有找到相关书籍</h3><p>换个书名，或确认书籍 ID 是否正确。</p></div>`;
      return;
    }
    drawBookCandidates(res.books, null, accountId, epoch, host);
  } catch (e) {
    if (currentImport(accountId, epoch, host)) {
      host.innerHTML = `<div class="note warn">${icon('alert')}<div>查询失败：${escapeHtml(e.message)}</div></div>`;
    }
  } finally {
    if (epoch === importEpoch) setLoading(btn, false);
  }
}

// 从 bookDetail 返回的章节列表算付费结构（price≠0 且 paid=0 视为付费未购）。
function auditFromChapters(chapters) {
  const a = { total: chapters.length, priced: 0, owned: 0 };
  for (const c of chapters) {
    if (c.paid) { a.owned++; continue; }
    if (Number(c.price) !== 0) a.priced++;
  }
  return a;
}

function drawBookCandidates(books, audit, accountId, epoch, host) {
  if (!currentImport(accountId, epoch, host)) return;
  host.innerHTML = `
    <div class="list" style="margin-top:12px">
      ${books.map((b, i) => {
        const a = audit || null;
        return `
        <div class="list-row" data-book-cand="${escapeHtml(b.id)}">
          <div class="shelf-cover" style="width:38px;height:52px">${b.cover ? `<img loading="lazy" src="${escapeHtml(b.cover)}" alt="">` : ''}</div>
          <div class="list-main">
            <div class="list-title">${escapeHtml(b.title || b.id)}</div>
            <div class="list-sub">${escapeHtml(b.author || '')} · <span class="mono">${escapeHtml(b.id)}</span></div>
          </div>
          ${a ? `<span class="badge ${payBadge(a).cls}">${payBadge(a).text}</span>` : ''}
          <button class="btn btn-sm btn-primary" data-import-book="${escapeHtml(b.id)}"
            data-title="${escapeHtml(b.title || '')}" data-author="${escapeHtml(b.author || '')}"
            data-cover="${escapeHtml(b.cover || '')}">${icon('plus')}导入</button>
        </div>`;
      }).join('')}
    </div>
  `;
  $$('[data-import-book]', host).forEach((btn) => btn.addEventListener('click', async () => {
    if (!currentImport(accountId, epoch, host)) return;
    const bookId = btn.dataset.importBook;
    const b = {
      book_id: bookId,
      title: btn.dataset.title || '',
      author: btn.dataset.author || '',
      cover: btn.dataset.cover || '',
    };
    setLoading(btn, true);
    try {
      // 拉一次目录核对付费结构：除非开了"全本导入"，全书付费未购的书直接拒绝。
      const detail = await api.bookDetail(accountId, bookId);
      if (!currentImport(accountId, epoch, host)) return;
      const audit = auditFromChapters(detail.chapters || []);
      const ignorePaid = Boolean($('#shelf-ignore-paid', $('#page'))?.checked);
      const allPaid = audit.total > 0 && audit.priced === audit.total;
      if (allPaid && !ignorePaid) {
        toast(`「${b.title || bookId}」全书 ${audit.total} 章都要付费且未购买，无法导入（已购/会员请先勾选「全本导入」）`, 'error', 6000);
        return;
      }
      await importBooks([b], { accountOverride: accountId });
    } catch (e) {
      if (currentImport(accountId, epoch, host)) toast(e.message, 'error');
    } finally {
      setLoading(btn, false);
    }
  }));
}

// 付费结构徽标（书架一键导入用的快照结构）。
function auditLabel(a) {
  if (!a || !a.total) return { text: '未知', cls: 'neutral', unpaid: false };
  if (a.priced === a.total) return { text: '未付费', cls: 'err', unpaid: true };
  if (a.priced > 0) return { text: `部分付费(未购 ${a.priced} 章)`, cls: 'warn', unpaid: false };
  if (a.owned > 0) return { text: '已付费', cls: 'ok', unpaid: false };
  return { text: '免费', cls: 'ok', unpaid: false };
}

function payBadge(a) {
  if (!a || !a.total) return { text: '未知', cls: 'neutral' };
  if (a.priced === a.total) return { text: `未付费（${a.total} 章都要付费）`, cls: 'err' };
  if (a.priced > 0) return { text: `可读 ${a.total - a.priced} 章（付费未购 ${a.priced}）`, cls: 'warn' };
  return { text: `全本可读（${a.total} 章）`, cls: 'ok' };
}

// 拉取书架：先出勾选列表让用户审查，确认后才写入书池。
async function loadShelf() {
  const box = $('#page');
  const btn = $('#shelf-load', box);
  const accountId = Number($('#shelf-account', box).value || 0);
  const host = $('[data-import-result]', box);
  const epoch = ++importEpoch;
  shelfCache = null;
  host.innerHTML = '<div class="hint">正在拉取书架…</div>';
  setLoading($('#book-query-btn', box), false);
  setLoading(btn, true);
  try {
    const data = await api.poolShelf(accountId);
    if (!currentImport(accountId, epoch, host)) return;
    shelfCache = {
      ...data,
      account_id: accountId,
      books: data.books || [],
      archive_failures: data.archive_failures || {},
      archive_error: '',
    };
    shelfPage = 1;
    drawShelfPreview();
  } catch (e) {
    if (currentImport(accountId, epoch, host)) {
      host.innerHTML = `<div class="note warn">${icon('alert')}<div>拉取书架失败：${escapeHtml(e.message)}</div></div>`;
    }
  } finally {
    if (epoch === importEpoch) setLoading(btn, false);
  }
}

function archiveStatusHTML(snapshot) {
  const total = Number(snapshot.archive_total) || 0;
  const processed = Number(snapshot.archive_processed) || 0;
  const remaining = Math.max(0, total - processed);
  const failures = Object.entries(snapshot.archive_failures || {});
  const next = Number(snapshot.next_archive_offset ?? -1);
  if (!total && !failures.length && next < 0 && !snapshot.archive_error) return '';
  return `
    <div class="note ${failures.length || snapshot.archive_error ? 'warn' : ''}" style="margin-top:12px">
      ${icon('book')}<div>
        <b>归档进度：已处理 ${processed}/${total} 本</b> · 剩余 ${remaining} 本待处理
        ${snapshot.archive_error ? `<div style="margin-top:6px">加载中断：${escapeHtml(snapshot.archive_error)}。归档尚未全部加载。</div>` : ''}
        ${failures.length ? `<details style="margin-top:6px"><summary>${failures.length} 本归档书籍详情获取失败，点击查看 ID</summary>
          <ul style="margin:6px 0 0 18px;padding:0">${failures.map(([id, message]) => `<li><span class="mono">${escapeHtml(id)}</span>：${escapeHtml(message)}</li>`).join('')}</ul>
        </details>` : ''}
        ${next >= 0 ? `<button class="btn btn-sm" data-shelf-more style="margin-top:8px">${icon('plus')}${snapshot.archive_error ? '重试加载归档' : `加载更多归档（剩余 ${remaining} 本）`}</button>` : ''}
      </div>
    </div>
  `;
}

function redrawShelfPreservingSelection(host) {
  const checked = new Map($$('[data-shelf-book]', host).map((cb) => [cb.dataset.shelfBook, cb.checked]));
  drawShelfPreview();
  $$('[data-shelf-book]', host).forEach((cb) => {
    if (checked.has(cb.dataset.shelfBook)) cb.checked = checked.get(cb.dataset.shelfBook);
  });
  const count = $('#shelf-count', host);
  if (count) count.textContent = `(${$$('[data-shelf-book]:checked', host).length})`;
}

async function loadMoreArchive() {
  const snapshot = shelfCache;
  const host = $('[data-import-result]');
  const accountId = snapshot?.account_id;
  const epoch = importEpoch;
  const offset = Number(snapshot?.next_archive_offset ?? -1);
  if (!snapshot || snapshot.archive_loading || offset < 0 || !currentImport(accountId, epoch, host)) return;
  const btn = $('[data-shelf-more]', host);
  snapshot.archive_loading = true;
  setLoading(btn, true);
  try {
    const data = await api.poolShelf(accountId, offset, snapshot.archive_revision || '');
    if (snapshot !== shelfCache || !currentImport(accountId, epoch, host)) return;
    if (Number(data.account_id) !== accountId || data.archive_revision !== snapshot.archive_revision
      || (Number(data.next_archive_offset) >= 0 && Number(data.next_archive_offset) <= offset)) {
      throw new Error('归档分页状态已变化，请重新拉取书架');
    }
    const seen = new Set((snapshot.books || []).map((b) => b.id));
    for (const book of data.books || []) {
      if (!seen.has(book.id)) {
        snapshot.books.push(book);
        seen.add(book.id);
      }
    }
    snapshot.audit = { ...(snapshot.audit || {}), ...(data.audit || {}) };
    snapshot.in_pool = [...new Set([...(snapshot.in_pool || []), ...(data.in_pool || [])])];
    snapshot.archive_failures = { ...(snapshot.archive_failures || {}), ...(data.archive_failures || {}) };
    for (const book of data.books || []) delete snapshot.archive_failures[book.id];
    snapshot.archive_total = data.archive_total;
    snapshot.archive_processed = data.archive_processed;
    snapshot.next_archive_offset = data.next_archive_offset;
    snapshot.archive_revision = data.archive_revision;
    snapshot.archive_error = '';
  } catch (e) {
    if (snapshot === shelfCache && currentImport(accountId, epoch, host)) snapshot.archive_error = e.message;
  } finally {
    snapshot.archive_loading = false;
    if (snapshot === shelfCache && currentImport(accountId, epoch, host)) redrawShelfPreservingSelection(host);
    else setLoading(btn, false);
  }
}

// 书架审查列表：勾选需要导入的书（未付费的行禁用），点「导入选中」才开始写库。
function drawShelfPreview() {
  const host = $('[data-import-result]');
  if (!shelfCache || !host?.isConnected || Number($('#shelf-account')?.value || 0) !== shelfCache.account_id) return;
  const audit = shelfCache.audit || {};
  const inPool = new Set(shelfCache.in_pool || []);
  const books = shelfCache.books || [];
  const archiveStatus = archiveStatusHTML(shelfCache);
  if (!books.length) {
    const pendingArchive = Number(shelfCache.next_archive_offset ?? -1) >= 0 || Object.keys(shelfCache.archive_failures || {}).length > 0;
    host.innerHTML = `<div class="empty" style="padding:28px"><h3>${pendingArchive ? '当前还没有可导入的书籍' : '书架是空的'}</h3>
      <p>${pendingArchive ? '归档书籍尚未全部取得，详情见下方。' : '该账号的微信读书书架没有读到任何书籍。'}</p></div>${archiveStatus}`;
    $('[data-shelf-more]', host)?.addEventListener('click', loadMoreArchive);
    return;
  }
  const pages = Math.max(1, Math.ceil(books.length / shelfPageSize));
  if (shelfPage > pages) shelfPage = pages;
  const pageBooks = pageSlice(books, shelfPage, shelfPageSize);
  const rows = pageBooks.map((b) => {
    const lab = auditLabel(audit[b.id]);
    const blocked = lab.unpaid;
    return `
      <tr class="shelf-row" data-shelf="${escapeHtml(b.id)}" data-blocked="${blocked ? 1 : 0}">
        <td>${blocked
          ? '<input type="checkbox" disabled title="未付费，章节不可读，不导入">'
          : `<input type="checkbox" data-shelf-book="${escapeHtml(b.id)}" data-title="${escapeHtml(b.title)}" data-author="${escapeHtml(b.author || '')}" data-cover="${escapeHtml(b.cover || '')}">`}</td>
        <td><div class="shelf-cover">${b.cover ? `<img loading="lazy" src="${escapeHtml(b.cover)}" alt="">` : ''}</div></td>
        <td class="cell-strong">${escapeHtml(b.title)}</td>
        <td class="cell-muted">${escapeHtml(b.author || '—')}</td>
        <td><span class="badge ${lab.cls}">${lab.text}</span></td>
        <td>${inPool.has(b.id) ? '<span class="badge ok">已在池中</span>' : '<span class="badge neutral">未入池</span>'}</td>
      </tr>
    `;
  }).join('');
  host.innerHTML = `
    <div class="tool-row" style="margin-top:12px;align-items:center">
      <span class="hint">勾选要导入的书籍（未付费的不可选）</span>
      <button class="btn" id="shelf-reload">${icon('refresh')}重新拉取</button>
      <span style="flex:1"></span>
      <button class="btn btn-primary" id="shelf-import">${icon('plus')}导入选中 <span id="shelf-count"></span></button>
    </div>
    <div class="table-wrap">
      <table class="table shelf-table table-wide">
        <thead><tr><th style="width:34px"></th><th style="width:56px">封面</th><th>书名</th><th>作者</th><th style="width:150px">付费</th><th style="width:96px">书池</th></tr></thead>
        <tbody>${rows}</tbody>
      </table>
    </div>
    ${books.length > 10 ? pagerHTML(books.length, shelfPage, shelfPageSize) : ''}
    ${archiveStatus}
  `;
  bindPager(host, {
    onPage: (delta) => { shelfPage = Math.max(1, shelfPage + delta); drawShelfPreview(); },
    onSize: (size) => { shelfPageSize = size; shelfPage = 1; drawShelfPreview(); },
  });
  const recount = () => { $('#shelf-count', host).textContent = `(${String($$('[data-shelf-book]:checked', host).length)})`; };
  $$('[data-shelf-book]', host).forEach((cb) => cb.addEventListener('change', recount));
  $('#shelf-reload', host).addEventListener('click', () => loadShelf());
  $('#shelf-import', host).addEventListener('click', () => importSelected());
  $('[data-shelf-more]', host)?.addEventListener('click', loadMoreArchive);
  recount();
}

async function importSelected() {
  const host = $('[data-import-result]');
  const snapshot = shelfCache;
  const accountId = Number($('#shelf-account')?.value || 0);
  const epoch = importEpoch;
  if (!snapshot || snapshot.account_id !== accountId || !currentImport(accountId, epoch, host)) return;
  const selected = $$('[data-shelf-book]:checked', host).map((cb) => ({
    book_id: cb.dataset.shelfBook,
    title: cb.dataset.title || '',
    author: cb.dataset.author || '',
    cover: cb.dataset.cover || '',
  }));
  if (!selected.length) { toast('请先勾选要导入的书籍', 'warn'); return; }
  const ok = await confirmDialog({
    title: '导入选中书籍',
    message: `将把勾选的 ${selected.length} 本书写入所选账号的书池（含可读章节），未付费的书不会导入。`,
    confirmText: '导入',
  });
  if (!ok || snapshot !== shelfCache || !currentImport(accountId, epoch, host)) return;
  await importBooks(selected, { accountOverride: accountId, auditMap: snapshot.audit || {} });
}

/* ---------- 通用导入：进度弹窗 + 失败明细 ---------- */

async function importBooks(selected, { accountOverride = 0, auditMap = null } = {}) {
  const box = $('#page');
  const accountId = accountOverride || Number($('#shelf-account', box)?.value || 0);
  const snapshot = shelfCache?.account_id === accountId ? shelfCache : null;
  const withChapters = Boolean($('#shelf-check-chapters', box)?.checked);
  const per = Number($('#shelf-chapters-per', box)?.value || 0);
  const ignorePaid = Boolean($('#shelf-ignore-paid', box)?.checked);
  let aborted = false;
  const confirmedIDs = new Set();
  const failures = {};

  const updatePreview = () => {
    if (!snapshot || snapshot !== shelfCache || poolTab !== 'import'
      || Number($('#shelf-account')?.value || 0) !== accountId) return;
    snapshot.in_pool = [...new Set([...(snapshot.in_pool || []), ...confirmedIDs])];
    drawShelfPreview();
    drawImportFailures(failures, selected);
  };

  const { mask, close: closeModalFn } = openModal({
    title: '导入书池',
    subtitle: '逐本拉取目录并写入书池（含可读章节过滤）',
    narrow: true,
    body: `
      <div class="bar" style="height:10px;margin-bottom:10px"><i id="imp-bar" style="width:0%"></i></div>
      <div style="font-weight:640;font-size:13px" id="imp-label">准备导入 0/${selected.length}…</div>
      <div class="hint" id="imp-detail"></div>
    `,
    footer: `<button class="btn" id="imp-cancel">${icon('close')}取消导入</button>`,
    onClose: () => { aborted = true; },
  });
  const cancelBtn = $('#imp-cancel', mask);
  cancelBtn.addEventListener('click', () => {
    aborted = true;
    closeModalFn();
    updatePreview();
  });
  const bar = $('#imp-bar', mask);
  const label = $('#imp-label', mask);
  const detail = $('#imp-detail', mask);

  let added = 0, skipped = 0, chapterTotal = 0, chapterExisted = 0;
  for (let i = 0; i < selected.length; i++) {
    if (aborted) break;
    const b = selected[i];
    label.textContent = `导入 ${i + 1}/${selected.length}：${b.title || b.book_id}`;
    bar.style.width = `${Math.round((i / selected.length) * 100)}%`;
    detail.textContent = `已完成：书籍 +${added} · 章节 +${chapterTotal}${chapterExisted ? `（已在池 ${chapterExisted}）` : ''} · 异常 ${Object.keys(failures).length}`;
    try {
      const res = await api.poolShelfImport({
        account_id: accountId,
        books: [b],
        with_chapters: withChapters,
        chapters_per_book: per,
        ignore_paid: ignorePaid,
        audit: auditMap && auditMap[b.book_id] ? { [b.book_id]: auditMap[b.book_id] } : {},
      });
      added += (res.books_added || []).length;
      skipped += (res.books_skipped || []).length;
      chapterTotal += res.chapters_added || 0;
      chapterExisted += res.chapters_existed || 0;
      for (const id of [...(res.books_added || []), ...(res.books_skipped || [])]) confirmedIDs.add(id);
      Object.assign(failures, res.failures || {});
    } catch (e) {
      failures[b.book_id] = e.message;
    }
  }
  bar.style.width = '100%';
  label.textContent = aborted ? '已取消导入' : '导入完成';
  detail.innerHTML = `
    书籍 +${added}（补充信息 ${skipped}） · 章节 +${chapterTotal}${chapterExisted ? `（其中已在池 ${chapterExisted} 条）` : ''} · 异常 ${Object.keys(failures).length}
  `;

  const closeBtn = document.createElement('button');
  closeBtn.className = 'btn btn-primary';
  closeBtn.innerHTML = `${icon('check')}关闭`;
  closeBtn.addEventListener('click', () => {
    closeModalFn();
    updatePreview();
    refreshPoolDataIfLive();
  });
  cancelBtn.replaceWith(closeBtn);
  if (aborted) updatePreview();
}

function refreshPoolDataIfLive() {
  if (poolTab === 'manage') {
    refreshPoolData();
  } else {
    api.pool(manageAccount).then((d) => { poolCache = d; }).catch(() => {});
  }
}

// 导入失败/跳过明细：含未付费跳过与章节拉取原因，支持重试失败章节。
function drawImportFailures(failures, selected) {
  const host = $('[data-import-result]');
  if (!host) return;
  let box = $('[data-shelf-fails]', host);
  const ids = Object.keys(failures).filter((id) => failures[id]);
  if (!ids.length) {
    if (box) box.remove();
    return;
  }
  if (!box) {
    box = document.createElement('div');
    box.dataset.shelfFails = '1';
    host.insertAdjacentElement('beforeend', box);
  }
  const retriable = ids.filter((id) => !/未付费/.test(failures[id]));
  box.innerHTML = `
    <div class="note warn" style="margin-top:10px">${icon('alert')}<div>
      <b>${ids.length} 本书有提示</b>：
      <ul style="margin:6px 0 0 18px;padding:0">
        ${ids.map((id) => {
          const t = (selected.find((s) => s.book_id === id) || {}).title || '';
          return `<li><b>${escapeHtml(t || id)}</b> <span class="mono">${escapeHtml(id)}</span>：${escapeHtml(failures[id])}</li>`;
        }).join('')}
      </ul>
    </div></div>
    ${retriable.length ? `
    <div style="display:flex;gap:8px;margin-top:8px">
      <button class="btn" data-retry-chapters>${icon('refresh')}重试失败章节 (${retriable.length})</button>
    </div>` : ''}
  `;
  $('[data-retry-chapters]', box)?.addEventListener('click', async (e) => {
    const retryBtn = e.currentTarget;
    setLoading(retryBtn, true);
    try {
      await importBooks(retriable.map((id) => ({
        book_id: id,
        title: (selected.find((s) => s.book_id === id) || {}).title || '',
        author: (selected.find((s) => s.book_id === id) || {}).author || '',
        cover: (selected.find((s) => s.book_id === id) || {}).cover || '',
      })));
    } finally {
      setLoading(retryBtn, false);
    }
  });
}

/* ---------- 二级菜单二：书池管理（列表 + 多选批量删除 + 顺序书单排序） ---------- */

function drawManageTab(box) {
  const accounts = state.accounts;
  if (!accounts.length) {
    box.innerHTML = `
      <article class="card">
        <div class="card-head"><div><div class="card-title">${icon('book')}书池管理</div><div class="card-sub">书池条目归属账号；阅读只从当前账号的池中抽取，池为空时无法发起阅读</div></div></div>
        <div class="note warn">${icon('alert')}<div>还没有绑定微信读书账号。请先到「账号」页扫码绑定账号，再来这里管理书池。</div></div>
      </article>
    `;
    return;
  }
  if (!accounts.some((a) => a.id === manageAccount)) {
    manageAccount = accounts[0].id;
  }
  poolCache = null;
  box.innerHTML = `
    <article class="card">
      <div class="card-head"><div><div class="card-title">${icon('book')}书池管理</div><div class="card-sub">当前账号的书池（顺序书单/随机轮换都从这里取书）</div></div></div>
      <div class="tool-row" style="margin-bottom:8px;flex-wrap:wrap;gap:8px">
        <label class="hint" for="pool-account">账号</label>
        <select class="select" id="pool-account" style="min-width:200px;max-width:100%">
          ${accounts.map((a) => `<option value="${a.id}" ${a.id === manageAccount ? 'selected' : ''}>${escapeHtml(accountLabel(a))}${a.status === 'expired' ? '（已失效）' : ''}</option>`).join('')}
        </select>
        <span class="hint" id="pool-summary"></span>
        <span style="flex:1"></span>
        <button class="btn btn-danger" id="pool-del-batch" disabled>${icon('trash')}删除所选</button>
      </div>
      <div data-pool-box><div class="hint">加载中…</div></div>
    </article>
  `;
  $('#pool-account', box).addEventListener('change', (e) => {
    manageAccount = Number(e.target.value);
    poolPage = 1;
    poolCache = null;
    $('#pool-del-batch', box).disabled = true;
    $('#pool-summary', box).textContent = '';
    $('[data-pool-box]', box).innerHTML = '<div class="hint">加载中…</div>';
    refreshPoolData();
  });
  $('#pool-del-batch', box).addEventListener('click', () => batchDeleteBooks());
  refreshPoolData();
}

function refreshPoolData() {
  const poolBox = $('[data-pool-box]', $('#page'));
  if (!poolBox) return Promise.resolve();
  const accountId = manageAccount;
  const epoch = ++poolRequestEpoch;
  const current = () => epoch === poolRequestEpoch && accountId === manageAccount && poolBox.isConnected
    && poolBox === $('[data-pool-box]', $('#page'));
  return api.pool(accountId).then((data) => {
    if (!current()) return;
    poolCache = data;
    const books = data.books || [];
    const counts = data.chapter_counts || {};
    const seqPos = data.seq_pos || null;
    const onCount = books.filter((b) => b.enabled).length;
    const chapterOn = Object.values(counts).reduce((a, b) => a + b, 0);
    const summary = $('#pool-summary', $('#page'));
    if (summary) {
      summary.textContent = `书籍 ${onCount}/${books.length} 启用 · 关联章节 ${chapterOn} 条 · 箭头可调整顺序（顺序书单按此顺序逐本阅读）`;
    }
    if (!books.length) {
      poolBox.innerHTML = `
        <div class="empty" style="padding:28px"><h3>书池是空的</h3>
        <p>切到「书籍导入」一键导入书架，或按书籍 ID / 书名查询添加。</p></div>
      `;
      return;
    }
    const progressOf = (i) => seqProgress(books, seqPos, i);
    const pages = Math.max(1, Math.ceil(books.length / poolPageSize));
    if (poolPage > pages) poolPage = pages;
    const pageBooks = pageSlice(books, poolPage, poolPageSize);
    const indexBase = (poolPage - 1) * poolPageSize;
    poolBox.innerHTML = `
      <div class="table-wrap pool-book-table">
        <table class="table">
          <thead><tr>
            <th style="width:30px"><input type="checkbox" data-book-sel-all title="全选"></th>
            <th style="width:56px">封面</th>
            <th>书籍（书名前徽标 = 顺序书单的阅读顺序）</th>
            <th style="width:110px">章节</th>
            <th style="width:140px">进度</th>
            <th style="width:190px">状态</th>
            <th style="width:104px"></th>
          </tr></thead>
          <tbody>${pageBooks.map((b, i) => poolBookRowHTML(b, counts[b.value] || 0, indexBase + i, progressOf(indexBase + i))).join('')}</tbody>
        </table>
      </div>
      ${books.length > 10 ? pagerHTML(books.length, poolPage, poolPageSize) : ''}
    `;
    bindPager(poolBox, {
      onPage: (delta) => { poolPage = Math.max(1, poolPage + delta); refreshPoolData(); },
      onSize: (size) => { poolPageSize = size; poolPage = 1; refreshPoolData(); },
    });
    wirePoolRows();
  }).catch((e) => {
    if (current()) {
      poolBox.innerHTML = `<div class="note warn">${icon('alert')}<div>书池加载失败：${escapeHtml(e.message)}</div></div>`;
    }
  });
}

// seqProgress 根据顺序书单的持久化指针（bi/ci）推断每本书的已读进度：
// 返回 {read: 已读完章节数, done: 是否整本读完}；无进度时 read=-1。
function seqProgress(books, seqPos, idx) {
  const read = { read: -1, done: false };
  if (!seqPos || seqPos.bi == null) return read;
  if (idx < seqPos.bi) {
    return { read: 0, done: true };
  }
  if (idx === seqPos.bi) {
    return { read: seqPos.ci, done: false };
  }
  return { read: 0, done: false };
}

function poolBookRowHTML(b, chapterCount, order, progress) {
  const prog = progress || { read: -1, done: false };
  return `
    <tr data-book-id="${b.id}" data-value="${escapeHtml(b.value)}">
      <td><input type="checkbox" data-book-sel="${b.id}" aria-label="选择 ${escapeHtml(b.title || b.value)}"></td>
      <td><div class="shelf-cover" style="width:38px;height:52px;cursor:pointer" data-book-open="${escapeHtml(b.value)}">
        ${b.cover ? `<img loading="lazy" src="${escapeHtml(b.cover)}" alt="">` : `${icon('bookOpen')}`}
      </div></td>
      <td>
        <div>
          <span class="badge neutral mono" title="顺序书单中的阅读顺序">#${order + 1}</span>
          <span class="cell-strong" data-book-open="${escapeHtml(b.value)}"
            title="${escapeHtml(b.title || b.value)}">${escapeHtml(b.title || '(未命名)')}</span>
        </div>
        <div class="cell-muted mono">${escapeHtml(b.value)}</div>
      </td>
      <td>${b.enabled
        ? `<button class="btn btn-sm btn-ghost" data-book-chapters-btn="${escapeHtml(b.value)}">${icon('list')}${chapterCount} 章</button>`
        : `<span class="cell-muted">—</span>`}</td>
      <td>${b.enabled && prog.read >= 0
        ? (prog.done
          ? '<span class="badge ok">已读完 ✓</span>'
          : `<span class="badge neutral mono" title="顺序书单中该书的已读章节数 / 总章节数">已读 ${prog.read}/${chapterCount}</span>`)
        : (b.enabled ? `<span class="badge neutral">未开始</span>` : `<span class="cell-muted">—</span>`)}</td>
      <td>
        <div class="cell-flex" style="display:flex;align-items:center;gap:8px;flex-wrap:wrap;justify-content:center">
          <label class="switch" title="参与/不参与轮换抽取">
            <input type="checkbox" data-book-enabled="${b.id}" ${b.enabled ? 'checked' : ''}><i></i>
          </label>
        </div>
      </td>
      <td style="text-align:center;white-space:nowrap">
        <button class="btn btn-sm btn-ghost btn-icon" data-book-move-up="${b.id}" title="上移">${icon('chevronUp')}</button>
        <button class="btn btn-sm btn-ghost btn-icon" data-book-move-down="${b.id}" title="下移">${icon('chevronDown')}</button>
        <button class="btn btn-sm btn-danger" data-book-del="${b.id}" title="删除书籍">${icon('trash')}</button>
      </td>
    </tr>
  `;
}

function wirePoolRows() {
  const page = $('#page');
  const delBatchBtn = $('#pool-del-batch', page);
  const recountSelection = () => {
    const n = $$('[data-book-sel]:checked', page).length;
    if (delBatchBtn) {
      delBatchBtn.disabled = n === 0;
      delBatchBtn.innerHTML = `${icon('trash')}删除所选${n ? ` (${n})` : ''}`;
    }
  };
  $$('[data-book-sel]', page).forEach((cb) => cb.addEventListener('change', recountSelection));
  $('[data-book-sel-all]', page)?.addEventListener('change', (e) => {
    $$('[data-book-sel]', page).forEach((cb) => { cb.checked = e.target.checked; });
    recountSelection();
  });

  // 顺序调整：顺序书单模式按这里的顺序逐本阅读。
  const moveBook = async (btn, direction) => {
    const id = Number(btn.dataset.bookMoveUp || btn.dataset.bookMoveDown);
    setLoading(btn, true);
    try {
      await api.poolMove(id, direction);
      refreshPoolData();
    } catch (e) {
      toast(e.message, 'error');
      setLoading(btn, false);
    }
  };
  $$('[data-book-move-up]', page).forEach((btn) => btn.addEventListener('click', () => moveBook(btn, -1)));
  $$('[data-book-move-down]', page).forEach((btn) => btn.addEventListener('click', () => moveBook(btn, 1)));

  // 书级开关：不参与轮换（仅本视角内，不删书、不删章节）。
  $$('[data-book-enabled]', page).forEach((cb) => cb.addEventListener('change', async () => {
    try {
      await api.poolUpdate(cb.dataset.bookEnabled, { enabled: cb.checked });
      toast(cb.checked ? '已设为参与轮换' : '已设为不参与轮换（书籍与章节保留在池中）', 'ok');
      refreshPoolData();
    } catch (e) {
      toast(e.message, 'error');
      refreshPoolData();
    }
  }));

  $$('[data-book-open]', page).forEach((el) => el.addEventListener('click', () => {
    const value = el.dataset.bookOpen;
    const book = (poolCache?.books || []).find((b) => b.value === value);
    if (book) openChaptersDrawer(book, manageAccount);
  }));
  $$('[data-book-chapters-btn]', page).forEach((btn) => btn.addEventListener('click', () => {
    const book = (poolCache?.books || []).find((b) => b.value === btn.dataset.bookChaptersBtn);
    if (book) openChaptersDrawer(book, manageAccount);
  }));
  $$('[data-book-del]', page).forEach((btn) => btn.addEventListener('click', async () => {
    const book = (poolCache?.books || []).find((b) => b.id === Number(btn.dataset.bookDel));
    if (!book) return;
    const chapters = (poolCache?.chapter_counts || {})[book.value] || 0;
    const ok = await confirmDialog({
      title: '删除书籍',
      message: `确定删除「${book.title || book.value}」吗？${chapters ? `它关联的 ${chapters} 条章节也会一并删除。` : ''}`,
      confirmText: '删除',
      danger: true,
    });
    if (!ok) return;
    try {
      await api.poolDelete(book.id);
      toast('已删除', 'ok');
      refreshPoolData();
    } catch (e) { toast(e.message, 'error'); }
  }));
}

async function batchDeleteBooks() {
  const page = $('#page');
  const ids = $$('[data-book-sel]:checked', page).map((cb) => Number(cb.dataset.bookSel));
  if (!ids.length) return;
  const books = ids.map((id) => (poolCache?.books || []).find((b) => b.id === id)).filter(Boolean);
  const totalChapters = books.reduce((a, b) => a + ((poolCache?.chapter_counts || {})[b.value] || 0), 0);
  const ok = await confirmDialog({
    title: '批量删除书籍',
    message: `确定删除所选的 ${books.length} 本书吗？${totalChapters ? `它们关联的 ${totalChapters} 条章节也会一并删除。` : ''}`,
    confirmText: '删除',
    danger: true,
  });
  if (!ok) return;
  let done = 0, failed = 0;
  for (const id of ids) {
    try {
      await api.poolDelete(id);
      done++;
    } catch {
      failed++;
    }
  }
  toast(`批量删除完成：成功 ${done}${failed ? `，失败 ${failed}` : ''}`, failed ? 'warn' : 'ok');
  refreshPoolData();
}

/* ---------- 章节抽屉 ---------- */

async function openChaptersDrawer(book, accountID) {
  const { mask, close } = openModal({
    title: book.title || book.value,
    subtitle: `${book.author || '微信读书书籍'} · 以下启用的章节参与轮换抽取`,
    wide: true,
    body: `<div data-chap-body><div class="hint">加载中…</div></div>`,
    footer: `
      <button class="btn" data-chap-bulk>${icon('plus')}手动添加章节</button>
      <button class="btn" data-chap-prune>${icon('refresh')}清理付费未购章节</button>
      <span style="flex:1"></span>
      <button class="btn btn-primary" data-chap-add-all>${icon('list')}从官网补拉章节</button>
    `,
  });

  const load = async () => {
    const box = $('[data-chap-body]', mask);
    try {
      const res = await api.poolBookChapters(book.value, { refresh: true, account_id: accountID });
      const chapters = res.chapters || [];
      const shown = chapters.slice(0, 300);
      let liveHTML = '';
      if (res.live) {
        const { total: lt, priced: lp } = res.live;
        const free = lt - lp;
        liveHTML = `
          <div class="note ${lp ? 'warn' : ''}" style="margin-bottom:8px">${icon('book')}<div>
            官方目录实时核对：全书 <b>${lt}</b> 章，免费/已解锁 <b>${free}</b> 章${lp ? `，<b>付费未购 ${lp}</b> 章` : ''}。
            ${lp ? '付费未购的章节读不开，心跳对这些章节不一定计入；可以点下方「清理付费未购章节」把它们从池中移除。' : '该书目录整体可读。'}
          </div></div>
        `;
      } else if (res.live_error) {
        liveHTML = `<div class="hint" style="margin-bottom:8px">官方目录核对失败：${escapeHtml(res.live_error)}</div>`;
      }
      box.innerHTML = `
        ${liveHTML}
        <div class="tool-row" style="margin-bottom:8px;align-items:center">
          <span class="hint">启用 ${chapters.filter((c) => c.enabled).length} / ${chapters.length}</span>
          <span style="flex:1"></span>
        </div>
        <div class="table-wrap" style="max-height:46vh;overflow-y:auto;padding:0 2px">
          <table class="table">
            <thead><tr><th>章节</th><th style="width:76px">字数</th><th style="width:92px">付费</th><th style="width:80px">启用</th><th style="width:56px"></th></tr></thead>
            <tbody>
              ${shown.map((c) => {
                const disp = (c.title || '').includes(' · ') ? c.title.split(' · ').slice(1).join(' · ') : (c.title || '(未命名章节)');
                const unpaid = res.unpaid && c.value ? res.unpaid[c.value] === true : null;
                const payBadge = unpaid === true
                  ? '<span class="badge err">付费未购</span>'
                  : unpaid === false
                    ? '<span class="badge ok">非付费</span>'
                    : '<span class="badge neutral">—</span>';
                return `
                <tr>
                  <td>
                    <div>${escapeHtml(disp)}</div>
                    <code class="mono chap-id">${escapeHtml(c.value)}</code>
                  </td>
                  <td class="cell-muted mono">${c.word_count ? escapeHtml(String(c.word_count)) : '—'}${c.word_count ? '<span class="cell-muted"> 字</span>' : ''}</td>
                  <td>${payBadge}</td>
                  <td><input type="checkbox" data-chap-enabled="${c.id}" ${c.enabled ? 'checked' : ''}></td>
                  <td><button class="btn btn-sm btn-danger" data-chap-del="${c.id}" title="删除章节">${icon('trash')}</button></td>
                </tr>
              `; }).join('') || '<tr><td colspan="5" class="hint">这本书还没有章节条目。点下方按钮从官网补拉可读章节，或手动添加章节 ID。</td></tr>'}
              ${chapters.length > shown.length ? `<tr><td colspan="5" class="hint">已显示前 300 条，其余 ${chapters.length - 300} 条仍在池中参与轮换抽取。</td></tr>` : ''}
            </tbody>
          </table>
        </div>
      `;
      $$('[data-chap-enabled]', box).forEach((cb) => cb.addEventListener('change', async () => {
        try {
          await api.poolUpdate(cb.dataset.chapEnabled, { enabled: cb.checked });
        } catch (e) { toast(e.message, 'error'); }
        load();
        refreshPoolData();
      }));
      $$('[data-chap-del]', box).forEach((btn) => btn.addEventListener('click', async () => {
        try {
          await api.poolDelete(btn.dataset.chapDel);
          load();
          refreshPoolData();
        } catch (e) { toast(e.message, 'error'); }
      }));
    } catch (e) {
      box.innerHTML = `<div class="note warn">${icon('alert')}<div>章节加载失败：${escapeHtml(e.message)}</div></div>`;
    }
  };
  await load();

  $('[data-chap-bulk]', mask).addEventListener('click', () => {
    bulkImportDialog('chapter', () => { load(); refreshPoolData(); }, accountID, book.value);
  });

  $('[data-chap-prune]', mask).addEventListener('click', async (e) => {
    const btn = e.currentTarget;
    const acc = accountID > 0 ? activeAccounts().find((a) => a.id === accountID) : activeAccounts()[0];
    if (!acc) { toast('需要一个登录可用的账号来核对官方目录', 'warn'); return; }
    const ok = await confirmDialog({
      title: '清理付费未购章节',
      message: '对照官方目录，把这本书在池中付费且未购买的章节全部删除（免费与已解锁章节保留，不会拉取新章节）。',
      confirmText: '清理',
      danger: true,
    });
    if (!ok) return;
    setLoading(btn, true);
    try {
      const res = await api.poolBookPrune(book.value, acc.id);
      const n = res.removed_count || 0;
      toast(n ? `已清理 ${n} 条付费未购章节` : '没有需要清理的付费未购章节', n ? 'ok' : 'info');
      load();
      refreshPoolData();
    } catch (e2) {
      toast(e2.message, 'error');
    } finally {
      setLoading(btn, false);
    }
  });

  $('[data-chap-add-all]', mask).addEventListener('click', async (e) => {
    const btn = e.currentTarget;
    const acc = accountID > 0 ? activeAccounts().find((a) => a.id === accountID) : activeAccounts()[0];
    if (!acc) { toast('需要一个登录可用的账号来拉取目录', 'warn'); return; }
    setLoading(btn, true);
    try {
      const res = await api.poolShelfImport({
        account_id: acc.id,
        books: [{ book_id: book.value, title: book.title, author: book.author, cover: book.cover }],
        with_chapters: true, chapters_per_book: 0,
      });
      const n = res.chapters_added || 0;
      const fails = res.failures || {};
      if (fails[book.value]) toast(`补拉失败：${fails[book.value]}`, 'warn', 7000);
      else toast(`已补拉 ${n} 个章节`, 'ok');
      load();
      refreshPoolData();
    } catch (e2) { toast(e2.message, 'error'); }
    finally { setLoading(btn, false); }
  });
}

/* ---------- 章节手动导入弹窗（章节抽屉内使用） ---------- */

function bulkImportDialog(kind, done, accountId = 0, bookId = '') {
  const { mask, close } = openModal({
    title: kind === 'book' ? '批量导入书籍 ID' : '批量导入章节 ID',
    subtitle: accountId > 0 ? '写入当前账号的书池' : '写入书池',
    narrow: true,
    body: `<textarea class="input mono" id="bulk-ids" rows="6" placeholder="粘贴多个 ID，用空格 / 逗号分隔"></textarea>`,
    footer: `<button class="btn btn-primary" id="bulk-ok">${icon('check')}导入</button>`,
  });
  $('#bulk-ok', mask).addEventListener('click', async () => {
    const text = $('#bulk-ids', mask).value;
    if (!text.trim()) { toast('请先粘贴 ID', 'warn'); return; }
    try {
      const res = await api.poolImport(kind, text, accountId, bookId);
      const a = (res.added || []).length, s = (res.skipped || []).length;
      close();
      toast(`导入完成：新增 ${a} 个，跳过重复 ${s} 个`, 'ok');
      done?.();
    } catch (e) { toast(e.message, 'error'); }
  });
}
