import { useState } from 'react'

export default function QuotaBar({ dept, onChange }) {
  const [editing, setEditing] = useState(false)
  const [val, setVal] = useState(dept.quota_total)
  const remaining = dept.quota_total - dept.quota_used
  const pct = dept.quota_total === 0 ? 0 : Math.round((dept.quota_used / dept.quota_total) * 100)

  return (
    <div className="quota">
      <div className="quota-info">
        <span>部门额度</span>
        <b className={remaining === 0 ? 'exhausted' : ''}>
          {dept.quota_used} / {dept.quota_total} 已借
        </b>
        <span className="muted">剩余 {remaining}</span>
        <button className="btn mini" onClick={() => { setVal(dept.quota_total); setEditing(true) }}>
          调整额度
        </button>
      </div>
      <div className="quota-track">
        <div className="quota-fill" style={{ width: `${pct}%` }} />
      </div>
      {editing && (
        <div className="inline-edit">
          <input
            type="number" min={dept.quota_used} value={val}
            onChange={(e) => setVal(Number(e.target.value))}
          />
          <button className="btn mini ok" onClick={() => { onChange(val); setEditing(false) }}>确认</button>
          <button className="btn mini" onClick={() => setEditing(false)}>取消</button>
          <small className="muted">不能低于当前借用数 {dept.quota_used};缩减不影响已借出的席位</small>
        </div>
      )}
    </div>
  )
}
