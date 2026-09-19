const BASE = '/api'

async function request(path, options = {}) {
  const res = await fetch(BASE + path, {
    headers: { 'Content-Type': 'application/json' },
    ...options
  })
  const data = await res.json().catch(() => ({}))
  if (!res.ok) {
    const err = new Error(data.message || `HTTP ${res.status}`)
    err.code = data.code
    err.status = res.status
    throw err
  }
  return data
}

export const api = {
  pool: () => request('/pool'),
  borrow: (body) => request('/borrow', { method: 'POST', body: JSON.stringify(body) }),
  doReturn: (credential) =>
    request('/return', { method: 'POST', body: JSON.stringify({ credential }) }),
  heartbeat: (credential, ttlSecond) =>
    request('/heartbeat', { method: 'POST', body: JSON.stringify({ credential, ttlSecond }) }),
  adjustQuota: (id, quota) =>
    request(`/departments/${id}/quota`, { method: 'PUT', body: JSON.stringify({ quota }) }),
  reap: () => request('/admin/reap', { method: 'POST' })
}

// 生成客户端幂等键（模拟申请方生成）。
export function uuid() {
  if (globalThis.crypto?.randomUUID) return globalThis.crypto.randomUUID()
  return 'rid-' + Math.random().toString(16).slice(2) + Date.now().toString(16)
}
