import { useState } from 'react'

function fmt(t) {
  return new Date(t).toLocaleString('zh-CN', { hour12: false })
}

const STATUS_STYLE = {
  ACTIVE: 'tag active',
  RETURNED: 'tag returned',
  EXPIRED: 'tag expired',
}

export default function PoolCard({ pool, now, onSeats }) {
  const [editing, setEditing] = useState(false)
  const [seats, setSeats] = useState(pool.seats_total)
  const online = pool.seats_total - pool.seats_available - pool.seats_offline
  const pct = pool.seats_total === 0
    ? 0
    : Math.round(((online + pool.seats_offline) / pool.seats_total) * 100)

  return (
    <div className="pool-card">
      <div className="pool-head">
        <div>
          <span className="product">{pool.product}</span>
          <span className="version">{pool.version}</span>
        </div>
        <div className="seat-edit">
          {editing ? (
            <>
              <input
                type="number" min="0" value={seats}
                onChange={(e) => setSeats(Number(e.target.value))}
              />
              <button className="btn mini ok" onClick={() => { onSeats(seats); setEditing(false) }}>保存</button>
              <button className="btn mini" onClick={() => setEditing(false)}>取消</button>
            </>
          ) : (
            <>
              <span className="seat-num">{pool.seats_total} 席</span>
              <button className="btn mini" onClick={() => { setSeats(pool.seats_total); setEditing(true) }}>调整席位</button>
            </>
          )}
        </div>
      </div>

      <div className="seat-bar">
        <div className="fill" style={{ width: `${pct}%` }} />
      </div>
      <div className="seat-legend">
        <span><i className="dot online" />在线 {online}</span>
        <span><i className="dot offline" />离线 {pool.seats_offline}</span>
        <span><i className="dot free" />可借 {pool.seats_available}</span>
      </div>

      <table className="occupy">
        <thead>
          <tr><th>员工</th><th>来源(主机 / IP)</th><th>模式</th><th>状态</th><th>到期</th></tr>
        </thead>
        <tbody>
          {pool.checkouts.length === 0 && (
            <tr><td colSpan="5" className="muted">暂无占用记录</td></tr>
          )}
          {pool.checkouts.map((c) => {
            const msLeft = new Date(c.expires_at).getTime() - now
            return (
              <tr key={c.id} className={c.status.toLowerCase()}>
                <td>{c.employee} <span className="muted">#{c.id}</span></td>
                <td className="src">{c.host || '—'} <span className="muted">{c.client_ip}</span></td>
                <td>
                  <span className={`mode ${c.mode.toLowerCase()}`}>{c.mode === 'OFFLINE' ? '离线' : '在线'}</span>
                </td>
                <td><span className={STATUS_STYLE[c.status]}>{c.status}</span></td>
                <td>
                  {fmt(c.expires_at)}
                  {c.status === 'ACTIVE' && (
                    <span className={msLeft < 60000 ? 'countdown urgent' : 'countdown'}>
                      {msLeft > 0 ? `剩 ${humanize(msLeft)}` : '已到期待回收'}
                    </span>
                  )}
                </td>
              </tr>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}

function humanize(ms) {
  const s = Math.floor(ms / 1000)
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m${s % 60}s`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h}h${m % 60}m`
  return `${Math.floor(h / 24)}d${h % 24}h`
}
