import { useState } from 'react'

function fmt(t) {
  return new Date(t).toLocaleString('zh-CN', { hour12: false })
}

export default function MyCredentials({ entries, now, onReturn }) {
  const [open, setOpen] = useState({})

  if (entries.length === 0) {
    return (
      <div className="card">
        <h3>本机凭证</h3>
        <p className="muted">
          暂无借出凭证。离线借出成功后,签名凭证会保存在这里(模拟员工本机凭证文件)。
        </p>
      </div>
    )
  }

  return (
    <div className="card">
      <h3>本机凭证 ({entries.length})</h3>
      {entries.map((e) => {
        const expired = new Date(e.expiresAt).getTime() <= now
        const expanded = open[e.key]
        return (
          <div key={e.key} className={`cred ${expired ? 'expired' : ''}`}>
            <div className="cred-head">
              <b>#{e.checkoutId} · {e.product}</b>
              <span className={`mode ${e.mode.toLowerCase()}`}>
                {e.mode === 'OFFLINE' ? '离线' : '在线'}
              </span>
            </div>
            <div className="cred-meta">
              <span>{e.employee}</span>
              <span className={expired ? 'expired-text' : ''}>
                {expired ? '已过期' : `到期 ${fmt(e.expiresAt)}`}
              </span>
            </div>
            <div className="cred-actions">
              <button
                className="btn mini ok"
                disabled={expired}
                title={expired ? '过期凭证无法归还,等待管理员回收' : '用凭证提前归还'}
                onClick={() => onReturn(e)}
              >
                {expired ? '凭证已过期' : '提前归还'}
              </button>
              <button className="btn mini ghost" onClick={() => setOpen({ ...open, [e.key]: !expanded })}>
                {expanded ? '隐藏凭证' : '查看凭证'}
              </button>
            </div>
            {expanded && (
              <pre className="token">{e.credential}</pre>
            )}
            {expired && (
              <small className="warn-text">
                过期后此凭证不再有效,席位只能由管理员"回收过期席位"释放。
              </small>
            )}
          </div>
        )
      })}
    </div>
  )
}
