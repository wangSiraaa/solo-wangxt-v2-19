import { useState } from 'react'
import { uuid } from '../api/client'

const PRESETS = {
  OFFLINE: [
    { label: '2 小时(演示)', sec: 2 * 3600 },
    { label: '7 天', sec: 7 * 24 * 3600 },
    { label: '30 天', sec: 30 * 24 * 3600 },
  ],
  ONLINE: [
    { label: '5 分钟心跳', sec: 300 },
    { label: '30 分钟', sec: 1800 },
  ],
}

export default function CheckoutForm({ pools, onSubmit }) {
  const [form, setForm] = useState({
    poolId: pools[0]?.pool.id || '',
    employee: '',
    host: typeof navigator !== 'undefined' ? 'WORKSTATION' : '',
    mode: 'OFFLINE',
    ttlSeconds: 2 * 3600,
  })
  const [busy, setBusy] = useState(false)
  const [lastKey, setLastKey] = useState(null)

  const selectedPool = pools.find((p) => p.pool.id === Number(form.poolId))

  const submit = async (retryKey) => {
    if (!form.employee || !form.poolId) return
    setBusy(true)
    const key = retryKey || uuid()
    setLastKey(key)
    // onSubmit 内部也会生成键,这里通过参数传入以支持"同键重试"演示
    await onSubmit({ ...form, _idemKey: key })
    setBusy(false)
  }

  const setMode = (mode) =>
    setForm((f) => ({ ...f, mode, ttlSeconds: PRESETS[mode][0].sec }))

  return (
    <div className="card form-card">
      <label>许可证池</label>
      <select value={form.poolId} onChange={(e) => setForm({ ...form, poolId: e.target.value })}>
        {pools.map(({ dept, pool }) => (
          <option key={pool.id} value={pool.id}>
            {dept.name} / {pool.product} (可借 {pool.seats_available}/{pool.seats_total})
          </option>
        ))}
      </select>

      <label>员工工号</label>
      <input
        value={form.employee}
        placeholder="例如 zhang.san"
        onChange={(e) => setForm({ ...form, employee: e.target.value })}
      />

      <label>占用来源(主机名)</label>
      <input value={form.host} onChange={(e) => setForm({ ...form, host: e.target.value })} />

      <div className="mode-switch">
        <button
          type="button"
          className={form.mode === 'OFFLINE' ? 'seg on offline' : 'seg'}
          onClick={() => setMode('OFFLINE')}
        >离线借出</button>
        <button
          type="button"
          className={form.mode === 'ONLINE' ? 'seg on online' : 'seg'}
          onClick={() => setMode('ONLINE')}
        >在线占用</button>
      </div>

      <label>{form.mode === 'OFFLINE' ? '离线有效期' : '心跳超时'}</label>
      <div className="ttl">
        {PRESETS[form.mode].map((p) => (
          <button
            type="button"
            key={p.sec}
            className={form.ttlSeconds === p.sec ? 'chip on' : 'chip'}
            onClick={() => setForm({ ...form, ttlSeconds: p.sec })}
          >{p.label}</button>
        ))}
      </div>
      <small className="muted">
        {form.mode === 'OFFLINE'
          ? '离线凭证保存到员工本机,到期前必须凭签名凭证归还。'
          : '在线席位按心跳计时,超时后由管理员回收。'}
      </small>

      <button className="btn primary block" disabled={busy} onClick={() => submit()}>
        {busy ? '处理中…' : '申请席位'}
      </button>
      {selectedPool && (
        <small className="muted">
          {selectedPool.pool.seats_available > 0
            ? `当前该池可借 ${selectedPool.pool.seats_available} 席`
            : '该池当前无空闲席位,并发申请只会有一人成功'}
        </small>
      )}
      {lastKey && (
        <button
          type="button"
          className="btn ghost block"
          disabled={busy}
          title="模拟网络重试:使用同一个幂等键再次请求,后端返回原借用结果"
          onClick={() => submit(lastKey)}
        >
          模拟网络重试(复用幂等键)
        </button>
      )}
    </div>
  )
}
