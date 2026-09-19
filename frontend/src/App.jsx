import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { api, uuid } from './api.js'

// 员工本地保存的"离线凭证钱包"：真实系统中是签名许可证文件，演示中存 localStorage。
const WALLET_KEY = 'licensepool.wallet.v1'

function loadWallet() {
  try {
    return JSON.parse(localStorage.getItem(WALLET_KEY) || '[]')
  } catch {
    return []
  }
}

function fmtTime(t) {
  if (!t) return '—'
  const d = new Date(t)
  return d.toLocaleString('zh-CN', { hour12: false })
}

function countdown(exp) {
  const ms = new Date(exp).getTime() - Date.now()
  if (ms <= 0) return '已到期'
  const s = Math.floor(ms / 1000)
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  const sec = s % 60
  return (h ? `${h}时` : '') + (m ? `${m}分` : '') + `${sec}秒`
}

export default function App() {
  const [pool, setPool] = useState(null)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [wallet, setWallet] = useState(loadWallet)
  const [, tick] = useState(0)

  const refresh = useCallback(async (silent) => {
    try {
      const data = await api.pool()
      setPool(data)
      setError('')
    } catch (e) {
      if (!silent) setError('加载池视图失败：' + e.message)
    }
  }, [])

  useEffect(() => {
    refresh()
    const id = setInterval(() => {
      refresh(true) // 管理员视角：在线占用/离线到期 2s 轮询
      tick((n) => n + 1)
    }, 2000)
    return () => clearInterval(id)
  }, [refresh])

  useEffect(() => {
    localStorage.setItem(WALLET_KEY, JSON.stringify(wallet))
  }, [wallet])

  const flash = (msg) => {
    setNotice(msg)
    setTimeout(() => setNotice(''), 4000)
  }

  return (
    <div className="page">
      <header>
        <h1>浮动许可证离线借出管理台</h1>
        <p className="sub">
          MySQL 事务 + 唯一约束控制席位 · Redis 仅缓存查询 · 签名本地凭证支持离线借出 /
          提前归还 / 过期回收
          {pool?.cached ? ' · 当前视图来自 Redis 缓存' : ' · 当前视图直查 MySQL'}
        </p>
      </header>

      {error && <div className="banner error">{error}</div>}
      {notice && <div className="banner ok">{notice}</div>}

      <PoolSections
        pool={pool}
        wallet={wallet}
        setWallet={setWallet}
        refresh={refresh}
        flash={flash}
        setError={setError}
      />
    </div>
  )
}

function PoolSections({ pool, wallet, setWallet, refresh, flash, setError }) {
  return (
    <div className="layout">
      <section className="card">
        <h2>① 许可证池与部门额度</h2>
        <p className="hint">部门负责人可调整额度：缩减只限制新申请，不抹掉现存借用。</p>
        <QuotaTable pool={pool} refresh={refresh} flash={flash} />
      </section>

      <section className="card">
        <h2>② 员工借用席位</h2>
        <BorrowForm pool={pool} wallet={wallet} setWallet={setWallet} refresh={refresh} flash={flash} />
      </section>

      <section className="card">
        <h2>③ 我的本地凭证（签名，离线可保存）</h2>
        <p className="hint">
          提前归还必须出示凭证；过期后凭证作废，席位由系统回收。在线占用可发心跳续期。
        </p>
        <Wallet wallet={wallet} setWallet={setWallet} pool={pool} refresh={refresh} flash={flash} />
      </section>

      <section className="card wide">
        <h2>④ 管理员：在线占用与离线到期来源</h2>
        <AdminBar refresh={refresh} flash={flash} />
        <OccupancyTable pool={pool} />
      </section>
    </div>
  )
}

function QuotaTable({ pool, refresh, flash }) {
  const [editing, setEditing] = useState({})
  if (!pool) return <p>加载中…</p>
  return (
    <table>
      <thead>
        <tr>
          <th>部门</th><th>额度</th><th>额度内占用</th><th>在线</th>
          <th>离线</th><th>可新申请</th><th>状态</th><th>调整额度</th>
        </tr>
      </thead>
      <tbody>
        {pool.departments.map((d) => (
          <tr key={d.deptId} className={d.oversubscribed ? 'over' : ''}>
            <td>{d.deptName}</td>
            <td><b>{d.quota}</b></td>
            <td>{d.activeInQuota} <span className="muted">(现存共 {d.activeTotal})</span></td>
            <td>{d.onlineActive}</td>
            <td>{d.offlineActive}</td>
            <td className={d.available > 0 ? 'avail' : 'zero'}>{d.available}</td>
            <td>
              {d.expiredActive > 0 && <span className="tag warn">待回收 {d.expiredActive}</span>}
              {d.oversubscribed && <span className="tag over">缩额后超额</span>}
              {!d.oversubscribed && d.expiredActive === 0 && <span className="tag ok">正常</span>}
            </td>
            <td>
              <input
                type="number" min="0" defaultValue={editing[d.deptId] ?? d.quota}
                onChange={(e) => setEditing({ ...editing, [d.deptId]: Number(e.target.value) })}
                style={{ width: 70 }}
              />
              <button className="sm" onClick={async () => {
                const q = editing[d.deptId] ?? d.quota
                try {
                  await api.adjustQuota(d.deptId, q)
                  await refresh()
                  flash(`「${d.deptName}」额度已调整为 ${q}`)
                } catch (e) { alert(e.message) }
              }}>应用</button>
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  )
}

function BorrowForm({ pool, wallet, setWallet, refresh, flash }) {
  const [deptId, setDeptId] = useState(pool?.departments[0]?.deptId ?? 0)
  const [employee, setEmployee] = useState('')
  const [offline, setOffline] = useState(false)
  const [ttl, setTtl] = useState(120)
  const [busy, setBusy] = useState(false)
  const [lastResult, setLastResult] = useState(null)
  const [concurrency, setConcurrency] = useState(1)
  const ridRef = useRef(uuid())

  useEffect(() => {
    if (!deptId && pool?.departments[0]) setDeptId(pool.departments[0].deptId)
  }, [pool, deptId])

  const doBorrow = async (rid) => {
    return api.borrow({
      deptId: deptId || pool.departments[0].deptId,
      employee: employee || '匿名员工',
      requestId: rid,
      offline,
      ttlSecond: ttl
    })
  }

  const submit = async (retry) => {
    setBusy(true)
    try {
      // retry=true 复用同一 requestId，模拟"网络重试"，应返回原借用结果。
      const rid = retry ? ridRef.current : uuid()
      ridRef.current = rid
      const res = await doBorrow(rid)
      setLastResult(res)
      if (!res.replayed) {
        setWallet((w) => [{
          credential: res.credential,
          token: res.token,
          employee: res.employee,
          deptId: res.deptId,
          seatSlot: res.seatSlot,
          offline: res.offline,
          issuedAt: res.issuedAt,
          expiresAt: res.expiresAt,
          spent: false
        }, ...w])
      }
      flash(res.replayed ? `网络重试成功：返回原借用结果（席位 #${res.seatSlot}）`
                         : `借用成功：获得席位 #${res.seatSlot}`)
      await refresh()
    } catch (e) {
      setLastResult({ error: e })
      if (e.code === 'NO_SEAT') flash('竞争失败：最后一个席位已被他人占用')
    } finally {
      setBusy(false)
    }
  }

  // 并发压测演示：同一部门多个员工同时抢席位，验证只有 1 人成功。
  const burst = async () => {
    setBusy(true)
    const results = await Promise.allSettled(
      Array.from({ length: concurrency }, (_, i) =>
        api.borrow({
          deptId: deptId || pool.departments[0].deptId,
          employee: `并发员工${i + 1}`,
          requestId: uuid(),
          offline,
          ttlSecond: ttl
        }))
    )
    const ok = results.filter((r) => r.status === 'fulfilled' && !r.value.replayed)
    const fail = results.filter((r) => r.status === 'rejected')
    setWallet((w) => [
      ...ok.map((r) => ({
        credential: r.value.credential, token: r.value.token, employee: r.value.employee,
        deptId: r.value.deptId, seatSlot: r.value.seatSlot, offline: r.value.offline,
        issuedAt: r.value.issuedAt, expiresAt: r.value.expiresAt, spent: false
      })),
      ...w
    ])
    flash(`并发 ${results.length} 人申请：${ok.length} 人成功，${fail.length} 人被唯一约束/事务拒绝`)
    await refresh()
    setBusy(false)
  }

  return (
    <div>
      <div className="form-row">
        <label>部门
          <select value={deptId} onChange={(e) => setDeptId(Number(e.target.value))}>
            {pool?.departments.map((d) => (
              <option key={d.deptId} value={d.deptId}>
                {d.deptName}（额度 {d.quota}，可借 {d.available}）
              </option>
            ))}
          </select>
        </label>
        <label>员工 <input value={employee} onChange={(e) => setEmployee(e.target.value)} placeholder="姓名" /></label>
        <label>模式
          <select value={offline ? 'offline' : 'online'} onChange={(e) => setOffline(e.target.value === 'offline')}>
            <option value="online">在线（可心跳续期）</option>
            <option value="offline">离线（凭证本地保存，到期前占用）</option>
          </select>
        </label>
        <label>时长(秒) <input type="number" min="1" max="86400" value={ttl} onChange={(e) => setTtl(Number(e.target.value))} /></label>
      </div>
      <div className="actions">
        <button disabled={busy} onClick={() => submit(false)}>借用席位</button>
        <button className="secondary" disabled={busy} onClick={() => submit(true)}>
          模拟网络重试（同 requestId）
        </button>
        <label className="inline">并发人数
          <input type="number" min="2" max="30" value={concurrency}
            onChange={(e) => setConcurrency(Number(e.target.value))} style={{ width: 60 }} />
        </label>
        <button className="warn" disabled={busy} onClick={burst}>并发抢最后一席</button>
      </div>
      {lastResult && (
        <div className={'result ' + (lastResult.error ? 'error-box' : 'ok-box')}>
          {lastResult.error ? (
            <>失败 [{lastResult.error.code}]：{lastResult.error.message}</>
          ) : (
            <>
              {lastResult.replayed ? '🔁 重试回放' : '✅ 借用成功'} ·
              席位 #{lastResult.seatSlot} · 到期 {fmtTime(lastResult.expiresAt)}
              <div className="cred">{lastResult.credential.slice(0, 80)}…</div>
            </>
          )}
        </div>
      )}
    </div>
  )
}

function Wallet({ wallet, setWallet, pool, refresh, flash }) {
  if (wallet.length === 0) return <p className="muted">本地凭证钱包为空，先在上方借用席位。</p>

  const deptName = (id) => pool?.departments.find((d) => d.deptId === id)?.deptName || `部门${id}`

  const act = async (item, fn) => {
    try {
      await fn()
      await refresh()
    } catch (e) {
      alert(`操作失败 [${e.code || 'ERROR'}]：${e.message}`)
    }
  }

  return (
    <table>
      <thead>
        <tr><th>员工</th><th>部门/席位</th><th>类型</th><th>到期</th><th>剩余</th><th>凭证(签名)</th><th>操作</th></tr>
      </thead>
      <tbody>
        {wallet.map((w) => {
          const expired = new Date(w.expiresAt).getTime() <= Date.now()
          return (
            <tr key={w.token} className={expired ? 'stale' : ''}>
              <td>{w.employee}</td>
              <td>{deptName(w.deptId)} #{w.seatSlot}</td>
              <td>{w.offline ? <span className="tag off">离线</span> : <span className="tag on">在线</span>}</td>
              <td>{fmtTime(w.expiresAt)}</td>
              <td>{countdown(w.expiresAt)}</td>
              <td className="mono" title={w.credential}>{w.credential.slice(0, 44)}…</td>
              <td>
                <button className="sm" onClick={() => act(w, async () => {
                  const res = await api.doReturn(w.credential)
                  if (res.status === 'returned' && !res.already) {
                    flash(`席位 #${w.seatSlot} 已提前归还`)
                    setWallet((cur) => cur.map((x) => x.token === w.token ? { ...x, spent: true } : x))
                  } else if (res.status === 'expired') {
                    flash('凭证已过期：席位由系统回收，旧凭证不能归还（重复归还结果一致）')
                  } else {
                    flash('该凭证已使用过：幂等返回原归还结果，席位未重复释放')
                  }
                })}>提前归还</button>
                {!w.offline && (
                  <button className="sm secondary" onClick={() => act(w, async () => {
                    const res = await api.heartbeat(w.credential, 300)
                    setWallet((cur) => cur.map((x) =>
                      x.token === w.token ? { ...x, expiresAt: res.expiresAt } : x))
                    flash('心跳续期成功，到期时间 +300 秒')
                  })}>心跳续期</button>
                )}
                <button className="sm danger" onClick={() => {
                  if (confirm('仅从本地钱包删除凭证；服务端借用不受影响（无凭证不能释放）。')) {
                    setWallet((cur) => cur.filter((x) => x.token !== w.token))
                  }
                }}>删除本地副本</button>
              </td>
            </tr>
          )
        })}
      </tbody>
    </table>
  )
}

function AdminBar({ refresh, flash }) {
  return (
    <div className="actions">
      <button className="secondary" onClick={async () => {
        const r = await api.reap()
        await refresh()
        flash(r.reaped > 0 ? `回收器立即执行：释放 ${r.reaped} 个到期席位` : '暂无到期席位')
      }}>立即执行过期回收</button>
      <button className="secondary" onClick={() => refresh()}>刷新</button>
      <span className="muted">回收器默认每 2 秒扫描一次；离线席位到期同样被回收，到期前无有效凭证不能释放。</span>
    </div>
  )
}

function OccupancyTable({ pool }) {
  const rows = pool?.checkouts || []
  if (!pool) return <p>加载中…</p>
  if (rows.length === 0) return <p className="muted">暂无借用记录。</p>
  return (
    <table>
      <thead>
        <tr>
          <th>Token</th><th>部门</th><th>席位</th><th>员工</th><th>来源</th>
          <th>状态</th><th>签发</th><th>到期</th><th>归还</th>
        </tr>
      </thead>
      <tbody>
        {rows.map((c) => (
          <tr key={c.token} className={
            c.status === 'expired' ? 'stale' : c.status === 'returned' ? 'done' : ''
          }>
            <td className="mono">{c.token.slice(0, 8)}</td>
            <td>{c.deptName}</td>
            <td>#{c.seatSlot}{!c.withinQuota && c.status === 'active' &&
              <span className="tag over" title="额度缩减后的遗留席位">额度外</span>}</td>
            <td>{c.employee}</td>
            <td>{c.offline ? <span className="tag off">离线占用</span> : <span className="tag on">在线占用</span>}</td>
            <td>
              {c.status === 'active' && !c.expired && <span className="tag on">占用中</span>}
              {c.status === 'active' && c.expired && <span className="tag warn">已到期待回收</span>}
              {c.status === 'expired' && <span className="tag stale">系统已回收</span>}
              {c.status === 'returned' && <span className="tag ok">已归还</span>}
            </td>
            <td>{fmtTime(c.issuedAt)}</td>
            <td>{fmtTime(c.expiresAt)}</td>
            <td>{fmtTime(c.returnedAt)}</td>
          </tr>
        ))}
      </tbody>
    </table>
  )
}
