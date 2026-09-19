import { useCallback, useEffect, useMemo, useState } from 'react'
import { api, uuid } from './api/client'
import PoolCard from './components/PoolCard.jsx'
import QuotaBar from './components/QuotaBar.jsx'
import CheckoutForm from './components/CheckoutForm.jsx'
import MyCredentials from './components/MyCredentials.jsx'

// 本地保存的凭证(模拟员工本机的离线凭证文件)
function loadCredentials() {
  try {
    return JSON.parse(localStorage.getItem('credentials') || '[]')
  } catch {
    return []
  }
}

export default function App() {
  const [data, setData] = useState({ departments: [] })
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [credentials, setCredentials] = useState(loadCredentials)
  const [now, setNow] = useState(Date.now())

  const refresh = useCallback(async (silent = false) => {
    try {
      if (!silent) setLoading(true)
      const v = await api.overview()
      setData(v)
      setError('')
    } catch (e) {
      setError(e.message)
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    refresh()
    const t = setInterval(() => refresh(true), 4000) // 轮询在线占用
    const clock = setInterval(() => setNow(Date.now()), 1000)
    return () => {
      clearInterval(t)
      clearInterval(clock)
    }
  }, [refresh])

  // 在线席位:定时用心跳凭证续租,模拟工程软件在线保活。
  // 心跳返回续期后的新凭证,替换本地保存的旧凭证。
  const beat = useCallback(async () => {
    const actives = credentials.filter((c) => c.mode === 'ONLINE')
    if (actives.length === 0) return
    let changed = false
    const next = [...credentials]
    for (const c of actives) {
      if (new Date(c.expiresAt).getTime() <= Date.now()) continue // 已过期不再心跳
      try {
        const r = await api.heartbeat(c.credential)
        const idx = next.findIndex((x) => x.key === c.key)
        if (idx >= 0) {
          next[idx] = { ...next[idx], credential: r.credential, expiresAt: r.checkout.expires_at }
          changed = true
        }
      } catch {
        /* 心跳失败:等下一轮,最终到期由回收器处理 */
      }
    }
    if (changed) persistCreds(next)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [credentials])

  useEffect(() => {
    const hb = setInterval(beat, 60000)
    return () => clearInterval(hb)
  }, [beat])

  const flash = (msg) => {
    setNotice(msg)
    setTimeout(() => setNotice(''), 4000)
  }

  const persistCreds = (next) => {
    setCredentials(next)
    localStorage.setItem('credentials', JSON.stringify(next))
  }

  // 借出:使用调用方传入的幂等键(支持"同键重试"演示)。
  // 后端保证同键重试返回原借用结果,不会二次扣减。
  const handleCheckout = async (form) => {
    const idem = form._idemKey || uuid()
    try {
      const res = await api.checkout({
        pool_id: Number(form.poolId),
        employee: form.employee,
        host: form.host,
        mode: form.mode,
        ttl_seconds: Number(form.ttlSeconds),
        idempotency_key: idem,
      })
      const cred = {
        key: idem,
        credential: res.credential,
        checkoutId: res.checkout.id,
        employee: res.checkout.employee,
        product: form.product,
        mode: res.checkout.mode,
        expiresAt: res.checkout.expires_at,
        replayed: res.replayed,
      }
      persistCreds([cred, ...credentials.filter((c) => c.key !== idem)])
      flash(
        res.replayed
          ? `网络重试命中幂等,返回原借用 #${res.checkout.id}`
          : `借出成功 #${res.checkout.id},凭证已保存到本机`,
      )
      refresh(true)
      return true
    } catch (e) {
      flash(`借出失败: ${e.message}`)
      return false
    }
  }

  const handleReturn = async (entry) => {
    try {
      await api.returnSeat(entry.credential)
      persistCreds(credentials.filter((c) => c.key !== entry.key))
      flash(`#${entry.checkoutId} 已提前归还,席位释放`)
      refresh(true)
    } catch (e) {
      // credential_expired / already_closed 等都不释放席位
      flash(`归还被拒: ${e.message} (code=${e.code})`)
    }
  }

  const handleSweep = async () => {
    try {
      const r = await api.sweep()
      flash(`过期回收完成,释放 ${r.reclaimed} 个席位`)
      refresh(true)
    } catch (e) {
      flash(`回收失败: ${e.message}`)
    }
  }

  const handleQuota = async (deptId, total) => {
    try {
      await api.setQuota(deptId, total)
      flash('部门额度已调整')
      refresh(true)
    } catch (e) {
      flash(`额度调整被拒: ${e.message}`)
    }
  }

  const handleSeats = async (poolId, seats) => {
    try {
      await api.setPoolSeats(poolId, seats)
      flash('池席位已调整')
      refresh(true)
    } catch (e) {
      flash(`席位调整被拒: ${e.message}`)
    }
  }

  const pools = useMemo(() => {
    const list = []
    for (const d of data.departments || []) {
      for (const p of d.pools || []) list.push({ dept: d, pool: p })
    }
    return list
  }, [data])

  const totals = useMemo(() => {
    let seats = 0; let online = 0; let offline = 0
    for (const d of data.departments || []) {
      for (const p of d.pools || []) {
        seats += p.seats_total
        online += p.seats_total - p.seats_available - p.seats_offline
        offline += p.seats_offline
      }
    }
    return { seats, online, offline, available: seats - online - offline }
  }, [data])

  return (
    <div className="page">
      <header className="topbar">
        <h1>浮动许可证管理台</h1>
        <div className="actions">
          <span className="stat">总席位 <b>{totals.seats}</b></span>
          <span className="stat online">在线 <b>{totals.online}</b></span>
          <span className="stat offline">离线 <b>{totals.offline}</b></span>
          <span className="stat free">可借 <b>{totals.available}</b></span>
          <button className="btn warn" onClick={handleSweep}>回收过期席位</button>
          <button className="btn" onClick={() => refresh()}>刷新</button>
        </div>
      </header>

      {notice && <div className="notice">{notice}</div>}
      {error && <div className="error-bar">后端连接失败: {error}</div>}
      {loading && <div className="loading">加载中…</div>}

      <main className="layout">
        <section className="col">
          <h2>许可证池与占用来源</h2>
          {(data.departments || []).map((d) => (
            <div key={d.id} className="dept-block">
              <div className="dept-head">
                <h3>{d.name}</h3>
                <QuotaBar dept={d} onChange={(v) => handleQuota(d.id, v)} />
              </div>
              {d.pools.length === 0 && <p className="muted">暂无许可证池</p>}
              {d.pools.map((p) => (
                <PoolCard
                  key={p.id}
                  pool={p}
                  now={now}
                  onSeats={(seats) => handleSeats(p.id, seats)}
                />
              ))}
            </div>
          ))}
        </section>

        <aside className="col side">
          <h2>员工借用</h2>
          <CheckoutForm pools={pools} onSubmit={handleCheckout} />
          <MyCredentials entries={credentials} now={now} onReturn={handleReturn} />
        </aside>
      </main>

      <footer className="foot">
        席位扣减仅在 MySQL 事务内完成(行锁 + 唯一约束);Redis 仅缓存查询结果(10s TTL,写后失效)。
      </footer>
    </div>
  )
}
