import { useEffect, useState } from 'react'

type Rollout = { id: string; service: string; scenario: string; phase: string; weight: number; errorRate: number; score: number; stableLatencyMs: number; canaryLatencyMs: number; reason: string; updatedAt: string }
type Event = { id: number; rolloutId: string; at: string; kind: string; message: string; weight: number; errorRate: number }
const API = import.meta.env.VITE_API_URL === undefined ? 'http://localhost:8080' : import.meta.env.VITE_API_URL

export default function App() {
  const [rollouts, setRollouts] = useState<Rollout[]>([])
  const [events, setEvents] = useState<Event[]>([])
  const [busy, setBusy] = useState(false)
  const [demoMode, setDemoMode] = useState(false)
  const [authRequired, setAuthRequired] = useState(false)
  const [tokenDraft, setTokenDraft] = useState('')
  const refresh = async (): Promise<boolean> => {
    try {
      const token = sessionStorage.getItem('canarylens-token') || ''
      const headers = token ? { Authorization: `Bearer ${token}` } : undefined
      const [r, e, c] = await Promise.all([fetch(`${API}/api/rollouts`, { headers, signal: AbortSignal.timeout(5000) }), fetch(`${API}/api/events`, { headers, signal: AbortSignal.timeout(5000) }), fetch(`${API}/api/config`, { headers, signal: AbortSignal.timeout(5000) })])
      if (r.status === 401 || e.status === 401 || c.status === 401) { setAuthRequired(true); return false }
      if (r.ok && e.ok && c.ok) { setAuthRequired(false); setRollouts(await r.json()); setEvents(await e.json()); setDemoMode(Boolean((await c.json()).demoMode)); return true }
      return false
    } catch { /* Controller can start after the dashboard. */ return false }
  }
  useEffect(() => {
    let timer = 0
    let delay = 2500
    let stopped = false
    const poll = async () => {
      const ok = await refresh()
      if (stopped) return
      delay = ok ? 2500 : Math.min(delay * 2, 30000)
      timer = window.setTimeout(poll, delay)
    }
    void poll()
    return () => { stopped = true; window.clearTimeout(timer) }
  }, [])
  const active = rollouts.find(x => ['Progressing', 'WaitingForMetrics', 'WaitingForEndpoints'].includes(x.phase))
  const latest = rollouts[0]
  const success = rollouts.length ? Math.round(100 * rollouts.filter(x => x.phase === 'Succeeded').length / rollouts.length) : 0
  async function start(scenario: 'bad' | 'healthy') {
    setBusy(true)
    try { const token = sessionStorage.getItem('canarylens-token') || ''; await fetch(`${API}/api/demo/start`, { method: 'POST', headers: { 'Content-Type': 'application/json', ...(token ? { Authorization: `Bearer ${token}` } : {}) }, body: JSON.stringify({ scenario }) }); await refresh() }
    finally { setBusy(false) }
  }
  return <main className="shell">
    {authRequired && <div className="auth-backdrop"><form className="auth-card" onSubmit={e => { e.preventDefault(); sessionStorage.setItem('canarylens-token', tokenDraft.trim()); void refresh() }}><div className="eyebrow">SECURE CONTROL PLANE</div><h2>Sign in to CanaryLens</h2><p>Enter the API bearer token provisioned by your cluster administrator. It stays in this browser tab.</p><input autoFocus type="password" value={tokenDraft} onChange={e => setTokenDraft(e.target.value)} placeholder="Bearer token"/><button className="primary" type="submit">Continue</button><small>Token is held in session storage and cleared when this tab closes.</small></form></div>}
    <header className="topbar"><a className="brand"><span className="mark">C</span>canary<span className="muted">lens</span></a><span className="online"><i/>CONTROL PLANE ONLINE</span></header>
    <section className="hero"><div><div className="eyebrow">RELEASE ENGINEERING / OVERVIEW</div><h1>Ship with a safety net.</h1><p>Progressive delivery with live signals and automatic recovery.</p></div>{demoMode && <div className="actions"><button disabled={busy} onClick={() => start('healthy')}>▶ &nbsp;Healthy release</button><button className="primary" disabled={busy} onClick={() => start('bad')}>↗ &nbsp;Simulate bad release</button></div>}</section>
    <section className="metrics"><Metric label="ACTIVE ROLLOUT" value={active ? `${active.weight}%` : '—'} foot={active ? `${active.service} · ${active.phase}` : 'No rollout in progress'} tone="purple"/><Metric label="LATEST RELEASE" value={latest?.phase ?? 'Ready'} foot={latest ? `${latest.service} · ${new Date(latest.updatedAt).toLocaleTimeString()}` : 'Waiting for first deployment'} tone="green"/><Metric label="CANARY ERROR RATE" value={latest ? `${(latest.errorRate * 100).toFixed(2)}%` : '0.00%'} foot="Threshold · 1.00% over 1 min" tone="amber"/><Metric label="SUCCESS RATE" value={`${success}%`} foot={`${rollouts.length} releases recorded`} tone="blue"/></section>
    <section className="columns"><article className="panel rollout"><div className="panel-head"><div><div className="eyebrow">LIVE DEPLOYMENT</div><h2>checkout-api <span className="badge">production</span></h2></div><span className={active ? 'status active' : 'status'}><i/>{active ? 'IN PROGRESS' : 'STABLE'}</span></div>
      <div className="release-row"><div className="release"><b className="stable">✓</b><span><strong>Stable</strong><small>v2.18.4 · baseline</small></span></div><div className="track"><div className="line"><i style={{ width: `${active?.weight ?? 0}%` }}/><b style={{ left: `${active?.weight ?? 0}%` }}/></div><div className="labels"><span>STABLE</span><span>CANARY · {active?.weight ?? latest?.weight ?? 0}%</span></div></div><div className="release"><b className="canary">↗</b><span><strong>Canary</strong><small>v2.19.0 · {latest?.scenario === 'bad' ? 'injected 5xx errors' : 'new release'}</small></span></div></div>
      <div className="chart-head"><strong>Error rate</strong><span> · rolling 1 minute</span><aside><i/> Canary <em/> 1% threshold</aside></div><Chart events={events}/><div className="rollout-foot"><span><i/>Checks every 15s</span><span>Rollback threshold <b>&gt; 1% 5xx</b></span><span>Scoring <b>gRPC · Python</b></span></div>
    </article><aside className="panel assessment"><div className="panel-head"><div><div className="eyebrow">SCORING SERVICE</div><h2>Release assessment</h2></div><span className="score">{latest ? Math.round(latest.score * 100) : '—'}<small>/100</small></span></div><div className="meter"><i style={{ width: `${Math.max(0, Math.min(100, (latest?.score ?? 0) * 100))}%` }}/></div><p className="reason">{latest?.reason ?? 'Start a rollout to compare canary and stable release signals.'}</p><div className="signal"><span>✓ &nbsp;Error-rate guardrail</span><b>1.00%</b></div><div className="signal"><span>✓ &nbsp;Latency comparison</span><b>{latest ? `${latest.stableLatencyMs.toFixed(0)}ms → ${latest.canaryLatencyMs.toFixed(0)}ms` : '—'}</b></div><div className="signal"><span>✓ &nbsp;Decision source</span><b>Prometheus + scorer</b></div><div className="note">✳ &nbsp;Score is advisory. The hard 1% error threshold controls rollback.</div></aside></section>
    <section className="panel history"><div className="panel-head"><div><div className="eyebrow">AUDIT TRAIL</div><h2>Rollout activity</h2></div><span className="eyebrow">{events.length} EVENTS</span></div><div className="table-wrap"><table><thead><tr><th>EVENT</th><th>RELEASE</th><th>CANARY TRAFFIC</th><th>ERROR RATE</th><th>TIME</th></tr></thead><tbody>{events.slice(0, 8).map(e => <tr key={e.id}><td><b className={`event ${e.kind}`}>{e.kind === 'rollback' ? '↶' : e.kind === 'completed' ? '✓' : '↗'}</b>{eventName(e.kind)}</td><td className="mono">{e.rolloutId.slice(-12)}</td><td className="mono">{e.weight}%</td><td className={e.errorRate > .01 ? 'bad' : 'good'}>{(e.errorRate * 100).toFixed(2)}%</td><td className="mono">{new Date(e.at).toLocaleTimeString()}</td></tr>)}{!events.length && <tr><td colSpan={5} className="empty">Rollout decisions will appear here.</td></tr>}</tbody></table></div></section>
    <footer>CANARYLENS · PROGRESSIVE DELIVERY CONTROL PLANE <span>DEMO ENVIRONMENT <i>●</i></span></footer>
  </main>
}
function Metric({label,value,foot,tone}:{label:string;value:string;foot:string;tone:string}) { return <article className="metric"><div className="eyebrow">{label}<b className={tone}>◉</b></div><strong>{value}</strong><small>{foot}</small></article> }
function eventName(kind:string) { return ({started:'Rollout started',sample:'Metrics checked',progressed:'Traffic increased',rollback:'Automatic rollback',completed:'Release promoted'} as Record<string,string>)[kind] ?? kind }
function Chart({events}:{events:Event[]}) { const points=events.slice().reverse().filter(e=>['sample','progressed','rollback','completed'].includes(e.kind)).slice(-8).map(e=>e.errorRate*100); const values=points.length ? points : []; const x=(i:number)=>20+i*560/Math.max(1,values.length-1); const y=(v:number)=>Math.max(12,Math.min(112,125-v*35)); const path=values.map((v,i)=>`${i?'L':'M'} ${x(i)} ${y(v)}`).join(' '); return <svg className="chart" viewBox="0 0 600 145" preserveAspectRatio="none"><line x1="0" y1="90" x2="600" y2="90" className="threshold"/>{values.length>0&&<path d={path} className="chart-line"/>}{values.map((v,i)=><circle key={i} cx={x(i)} cy={y(v)} r="3" className={v>1?'bad-point':'point'}/>)}<text x="580" y="84" textAnchor="end">1.0%</text><text x="0" y="140">−60s</text><text x="300" y="140" textAnchor="middle">−30s</text><text x="600" y="140" textAnchor="end">NOW</text></svg> }
