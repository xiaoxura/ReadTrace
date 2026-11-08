import { api } from './api.js';

export const state = {
  status: {},
  accounts: [],
  active: [],
  channels: [],
  schedules: [],
  tz: 'Asia/Shanghai',
  overview: null,
  page: 'dashboard',
  connected: false,
};

export async function refreshAccounts() {
  const res = await api.accounts();
  state.accounts = res.accounts || [];
  state.active = res.active || [];
  return res;
}

export async function refreshChannels() {
  const res = await api.pushChannels();
  state.channels = res.channels || [];
  return state.channels;
}

export async function refreshSchedules() {
  const res = await api.schedules();
  state.schedules = res.schedules || [];
  return state.schedules;
}

export function accountById(id) {
  return state.accounts.find((a) => a.id === Number(id));
}

export function accountName(id) {
  return accountById(id)?.name || `账号 #${id}`;
}
