async function request(path, { method = 'GET', body, signal } = {}) {
  const headers = {};
  if (body !== undefined) headers['Content-Type'] = 'application/json';
  if (method !== 'GET') headers['X-Requested-With'] = 'readtrace';

  let res;
  try {
    res = await fetch(path, {
      method,
      headers,
      credentials: 'same-origin',
      body: body !== undefined ? JSON.stringify(body) : undefined,
      signal,
    });
  } catch {
    throw new Error('网络连接失败，请确认服务仍在运行');
  }

  const text = await res.text();
  let data = null;
  try {
    data = text ? JSON.parse(text) : null;
  } catch {
    data = { error: text.slice(0, 160) || '响应格式错误' };
  }

  if (res.status === 401 && path !== '/api/login') {
    window.dispatchEvent(new CustomEvent('auth:expired'));
  }
  if (!res.ok) {
    throw new Error(data?.error || `请求失败（HTTP ${res.status}）`);
  }
  return data;
}

export const api = {
  status: () => request('/api/status'),
  setup: (password, siteName) => request('/api/setup', { method: 'POST', body: { password, site_name: siteName } }),
  login: (password) => request('/api/login', { method: 'POST', body: { password } }),
  logout: () => request('/api/logout', { method: 'POST', body: {} }),
  me: () => request('/api/me'),

  overview: (days = 14, accountId = 0) =>
    request(`/api/overview?days=${days}${accountId ? `&account_id=${accountId}` : ''}`),

  accounts: () => request('/api/accounts'),
  account: (id) => request(`/api/accounts/${id}`),
  importAccount: (payload) => request('/api/accounts', { method: 'POST', body: payload }),
  updateAccount: (id, patch) => request(`/api/accounts/${id}`, { method: 'PATCH', body: patch }),
  deleteAccount: (id) => request(`/api/accounts/${id}`, { method: 'DELETE' }),
  refreshAccount: (id) => request(`/api/accounts/${id}/refresh`, { method: 'POST', body: {} }),
  checkAccount: (id) => request(`/api/accounts/${id}/check`, { method: 'POST', body: {} }),
  savePayload: (id, payload) => request(`/api/accounts/${id}/payload`, { method: 'POST', body: payload }),
  clearPayload: (id) => request(`/api/accounts/${id}/payload`, { method: 'DELETE' }),

  qrStart: () => request('/api/qr', { method: 'POST', body: {} }),
  qrPoll: (id) => request(`/api/qr/${encodeURIComponent(id)}`),
  qrOtp: (id, code) => request(`/api/qr/${encodeURIComponent(id)}/otp`, { method: 'POST', body: { code } }),

  searchBooks: (accountId, q) =>
    request(`/api/books/search?account_id=${accountId}&q=${encodeURIComponent(q)}`),
  bookDetail: (accountId, bookId) =>
    request(`/api/books/${encodeURIComponent(bookId)}?account_id=${accountId}`),

  pool: (accountId = 0) => request(`/api/pool?account_id=${accountId}`),
  poolBookChapters: (bookId, { refresh = false, account_id = 0, limit = 50, offset = 0 } = {}) =>
    request(`/api/pool/book/${encodeURIComponent(bookId)}/chapters?account_id=${account_id}&limit=${limit}&offset=${offset}${refresh ? '&refresh=1' : ''}`),
  poolBookPrune: (bookId, account_id = 0) =>
    request(`/api/pool/book/${encodeURIComponent(bookId)}/prune?account_id=${account_id}`, { method: 'POST', body: {} }),
  poolAdd: (kind, value, title = '', accountId = 0, bookId = '') => request('/api/pool/add', { method: 'POST', body: { kind, value, title, account_id: accountId, book_id: bookId } }),
  poolImport: (kind, text, accountId = 0, bookId = '') => request('/api/pool/import', { method: 'POST', body: { kind, text, account_id: accountId, book_id: bookId } }),
  poolUpdate: (id, patch) => request(`/api/pool/items/${id}`, { method: 'PATCH', body: patch }),
  poolMove: (id, direction) => request(`/api/pool/items/${id}/move`, { method: 'POST', body: { direction } }),
  poolDelete: (id) => request(`/api/pool/items/${id}`, { method: 'DELETE' }),
  poolReset: (kind, accountId = 0) => request('/api/pool/reset', { method: 'POST', body: { kind, account_id: accountId } }),
  poolTemplate: (template) => request('/api/pool/template', { method: 'PUT', body: { template } }),
  poolShelf: (accountId = 0, archiveOffset = 0, archiveRevision = '') =>
    request(`/api/pool/shelf?account_id=${accountId}&archive_offset=${archiveOffset}${archiveRevision ? `&archive_revision=${encodeURIComponent(archiveRevision)}` : ''}`),
  poolShelfImport: (payload) => request('/api/pool/shelf/import', { method: 'POST', body: payload }),

  startRun: (accountId, minutes) => request('/api/runs', { method: 'POST', body: { account_id: accountId, minutes } }),
  stopRun: (id) => request(`/api/runs/${id}/stop`, { method: 'POST', body: {} }),
  runs: (accountId = 0, limit = 100) =>
    request(`/api/runs?limit=${limit}${accountId ? `&account_id=${accountId}` : ''}`),
  run: (id) => request(`/api/runs/${id}`),
  runLogs: (id, { afterId = 0, tail = false, limit = 500, signal } = {}) => {
    const q = new URLSearchParams({ after_id: String(afterId), limit: String(limit) });
    if (tail) q.set('tail', '1');
    return request(`/api/runs/${id}/logs?${q.toString()}`, { signal });
  },

  schedules: () => request('/api/schedules'),
  createSchedule: (payload) => request('/api/schedules', { method: 'POST', body: payload }),
  updateSchedule: (id, patch) => request(`/api/schedules/${id}`, { method: 'PATCH', body: patch }),
  deleteSchedule: (id) => request(`/api/schedules/${id}`, { method: 'DELETE' }),
  previewSchedule: (cron, n = 5) => request(`/api/schedules/preview?cron=${encodeURIComponent(cron)}&n=${n}`),

  pushChannels: () => request('/api/push/channels'),
  createPush: (payload) => request('/api/push/channels', { method: 'POST', body: payload }),
  updatePush: (id, patch) => request(`/api/push/channels/${id}`, { method: 'PATCH', body: patch }),
  deletePush: (id) => request(`/api/push/channels/${id}`, { method: 'DELETE' }),
  testPush: (id) => request(`/api/push/channels/${id}/test`, { method: 'POST', body: {} }),

  settings: () => request('/api/settings'),
  updateSettings: (payload) => request('/api/settings', { method: 'PUT', body: payload }),
  changePassword: (current, next) => request('/api/settings/password', { method: 'POST', body: { current, new: next } }),
};
