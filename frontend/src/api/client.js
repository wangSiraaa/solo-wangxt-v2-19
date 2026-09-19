// 与 Go Gin 后端交互的薄封装
async function request(path, options = {}) {
  const res = await fetch(path, {
    headers: { 'Content-Type': 'application/json' },
    ...options,
  })
  const body = await res.json().catch(() => ({}))
  if (!res.ok) {
    const err = new Error(body.message || `${res.status} ${res.statusText}`)
    err.code = body.code
    err.status = res.status
    throw err
  }
  return body
}

export const api = {
  overview: () => request('/api/overview'),
  sweep: () => request('/api/admin/sweep', { method: 'POST' }),
  createDepartment: (name, quota) =>
    request('/api/departments', {
      method: 'POST',
      body: JSON.stringify({ name, quota }),
    }),
  setQuota: (id, quota_total) =>
    request(`/api/departments/${id}/quota`, {
      method: 'PATCH',
      body: JSON.stringify({ quota_total }),
    }),
  setPoolSeats: (id, seats) =>
    request(`/api/pools/${id}/seats`, {
      method: 'PATCH',
      body: JSON.stringify({ seats }),
    }),
  checkout: (payload) =>
    request('/api/checkouts', { method: 'POST', body: JSON.stringify(payload) }),
  returnSeat: (credential) =>
    request('/api/returns', {
      method: 'POST',
      body: JSON.stringify({ credential }),
    }),
  heartbeat: (credential) =>
    request('/api/heartbeat', {
      method: 'POST',
      body: JSON.stringify({ credential }),
    }),
}

export function uuid() {
  if (crypto.randomUUID) return crypto.randomUUID()
  return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, (c) => {
    const r = (Math.random() * 16) | 0
    const v = c === 'x' ? r : (r & 0x3) | 0x8
    return v.toString(16)
  })
}
