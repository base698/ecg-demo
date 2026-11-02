package main

import "net/http"

func (c *controller) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(dashboardHTML))
}

const dashboardHTML = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Execution Compatibility Graph</title>
<style>
:root{--bg:#0d1117;--panel:#161b22;--line:#30363d;--fg:#e6edf3;--mut:#8b949e;--ok:#3fb950;--warn:#d29922;--bad:#f85149;--inv:#a371f7;--none:#484f58;--acc:#58a6ff;--blue:#1f6feb}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--fg);font:13px/1.45 ui-sans-serif,system-ui,-apple-system,Segoe UI,Roboto,sans-serif}
header{display:flex;align-items:center;gap:16px;padding:12px 20px;border-bottom:1px solid var(--line);background:var(--panel);position:sticky;top:0;z-index:5;flex-wrap:wrap}
header h1{font-size:16px;margin:0;font-weight:600}header h1 span{color:var(--mut);font-weight:400;margin-left:8px}
.stat{color:var(--mut)}.stat b{color:var(--fg);font-variant-numeric:tabular-nums}
.spacer{flex:1}button,input,select{font:inherit}select{background:#0d1117;color:var(--fg);border:1px solid var(--line);padding:4px;border-radius:6px}button{background:#21262d;border:1px solid var(--line);color:var(--fg);padding:4px 10px;border-radius:6px;cursor:pointer}button:hover{border-color:var(--acc)}
button.danger{color:var(--bad)}input[type=number]{width:60px;background:#0d1117;border:1px solid var(--line);color:var(--fg);padding:3px 6px;border-radius:6px}
a{color:var(--acc);text-decoration:none}
main{padding:16px 20px;display:grid;gap:16px;grid-template-columns:1fr 1fr}
section{background:var(--panel);border:1px solid var(--line);border-radius:10px;padding:14px 16px;min-width:0}
section.wide{grid-column:1/-1}section h2{font-size:13px;margin:0 0 10px;color:var(--mut);text-transform:uppercase;letter-spacing:.06em;font-weight:600;display:flex;align-items:center;gap:10px}
section h2 .hint{text-transform:none;letter-spacing:0;font-weight:400;font-size:12px}
.cols{display:flex;gap:14px;flex-wrap:wrap}.col{flex:1;min-width:220px}.col h3{margin:0 0 8px;font-size:12px;color:var(--mut)}
.card{border:1px solid var(--line);border-radius:8px;padding:8px 10px;margin-bottom:8px;background:#0d1117}
.card .top{display:flex;align-items:center;gap:8px}.card .id{font-weight:600}.card .meta{color:var(--mut);font-size:12px;margin-top:3px}
.card code{font-size:11px;color:var(--acc)}
.chip{display:inline-block;padding:1px 7px;border-radius:999px;font-size:11px;font-weight:600;border:1px solid transparent}
.chip.baseline{background:#1f6feb33;color:#79c0ff;border-color:#1f6feb66}.chip.candidate{background:#d2992233;color:#e3b341;border-color:#d2992266}
.chip.draining{background:#a371f733;color:#d2a8ff;border-color:#a371f766}.chip.retired,.chip.gone{background:#48405833;color:var(--mut);border-color:#484f58}
.chip.hold{color:var(--warn)}.chip.failed{color:var(--bad)}.chip.satisfied{color:var(--ok)}.chip.waiting{color:var(--mut)}
table{border-collapse:collapse;width:100%}th,td{text-align:left;padding:5px 8px;border-bottom:1px solid var(--line);font-variant-numeric:tabular-nums;white-space:nowrap}th{color:var(--mut);font-weight:600;font-size:11px;text-transform:uppercase;letter-spacing:.05em}
td.num{text-align:right}tr.dim td{color:var(--mut)}
.s-satisfied{color:var(--ok)}.s-partial{color:var(--warn)}.s-missing{color:var(--mut)}.s-failed{color:var(--bad)}.s-invalidated{color:var(--inv)}.s-unobserved,.s-exempt{color:var(--none)}.s-stale{color:var(--warn)}
.matrix td,.matrix th{text-align:center}.matrix td.cell{font-size:16px;cursor:default}.matrix td.rowhead{text-align:left;font-weight:600}
.legend{display:flex;gap:14px;flex-wrap:wrap;color:var(--mut);font-size:12px;margin-top:8px}.legend i{display:inline-block;width:18px;height:3px;vertical-align:middle;margin-right:5px;border-radius:2px}
svg text{font:12px ui-monospace,SFMono-Regular,Menlo,monospace;fill:var(--fg)}
.events{max-height:320px;overflow:auto;font-size:12px}.events div{padding:4px 0;border-bottom:1px solid var(--line)}.events .t{color:var(--mut);margin-right:8px}
.k-promote{color:var(--ok)}.k-retire{color:var(--inv)}.k-deploy{color:var(--warn)}.k-bootstrap{color:var(--acc)}
.recent{max-height:320px;overflow:auto}.bar{height:6px;border-radius:3px;background:#21262d;overflow:hidden;margin-top:4px}.bar i{display:block;height:100%;background:var(--ok)}
.ob{margin-bottom:14px}.ob .head{display:flex;gap:10px;align-items:center;margin-bottom:6px}
.s-covered{color:var(--acc)}.s-historical{color:var(--mut)}.s-unknown,.s-inconclusive,.chip.unknown{color:var(--warn)}.evidence-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(210px,1fr));gap:14px}.evidence-grid b{display:block;margin-bottom:4px}.evidence-grid p{margin:0;color:var(--mut);font-size:12px}.decision{border-left:3px solid var(--warn);padding:8px 12px;margin:8px 0}.scroll{overflow-x:auto}.gate td:first-child{white-space:normal;min-width:180px;max-width:280px}.reason{white-space:normal;min-width:180px;font-size:11px;color:var(--mut)}
.empty{color:var(--mut);padding:10px 0}
@media (max-width:1000px){main{grid-template-columns:1fr}}
</style></head><body>
<header>
  <h1>Execution Compatibility Graph <span>deployment evidence · explicit checks · promotion</span></h1>
  <span class="stat">traces <b id="st-traces">0</b></span>
  <span class="stat">spans <b id="st-spans">0</b></span>
  <span class="stat">gaps <b id="st-gaps">0</b></span>
  <span class="stat">route mismatches <b id="st-mism">0</b></span>
  <span class="spacer"></span>
  <label class="stat">coverage min <input type="number" id="thr" min="1"></label>
  <select id="mode" aria-label="Promotion policy"><option value="coverage">Demo coverage gate</option><option value="confidence">Confidence gate</option></select><button onclick="setThr()">apply</button>
  <button class="danger" onclick="reset()">reset evidence</button>
  <a href="/app" target="_blank">todo app ↗</a>
  <a href="http://localhost:16686" target="_blank">jaeger ↗</a>
</header>
<main>
 <section class="wide"><h2>What the evidence supports</h2><div id="capabilities"></div></section>
  <section class="wide"><h2>Instances <span class="hint">· deploy a new version with <code>./deploy.sh tasks 2</code> (or <code>./deploy.sh tasks 3 FAIL_RATE=0.3</code> for a regression)</span></h2><div class="cols" id="instances"></div></section>
<section class="wide gate"><h2>Promotion requirements <span class="hint">· exact full paths against the current baselines · one candidate per request</span></h2><div id="obligations"></div></section>
  <section class="wide"><h2>Versioned path graph <span class="hint">· traffic and prefix coverage overview</span></h2>
    <div id="graph"></div>
    <div class="legend"><span><i style="background:var(--ok)"></i>observed coverage</span><span><i style="background:var(--warn)"></i>below coverage minimum</span><span><i style="background:var(--bad)"></i>regression</span><span><i style="background:var(--none);border-top:2px dashed var(--none);height:0"></i>possible, unobserved</span><span><i style="background:var(--inv)"></i>historical (version unavailable)</span></div>
  </section>
  <section><h2>Prefix coverage matrix <span class="hint" id="matrix-hint"></span></h2><div id="matrix"></div>
    <div class="legend"><span class="s-satisfied">✓ observed rows covered</span><span class="s-partial">◐ executed, below requirement</span><span class="s-unobserved">◌ never executed</span><span class="s-failed">✗ regression</span><span class="s-invalidated">⊘ historical</span><span class="s-exempt">— exempt</span></div></section>

  <section class="wide"><h2>Execution observations <span class="hint">· fresh request counts and response checks · historical totals retained</span></h2><div id="paths"></div></section>
  <section class="wide"><h2>Library evidence <span class="hint">· normal todo traffic → SDK exporter → evidence container → Redis → shared evaluator</span></h2><div id="library"><div class="empty">Waiting for evidence service…</div></div></section>
  <section><h2>Events</h2><div class="events" id="events"></div></section>
  <section><h2>Recent traces <span class="hint">· credited only when complete and intended == observed</span></h2><div class="recent" id="recent"></div></section>
</main>
<script>
const $=s=>document.querySelector(s);
const esc=s=>String(s??'').replace(/[&<>"]/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[c]));
const SYM={covered:'✓',historical:'⊘',unknown:'?',inconclusive:'?',satisfied:'✓',partial:'◐',unobserved:'◌',missing:'◌',failed:'✗',invalidated:'⊘',exempt:'—',stale:'◷'};
const COLOR={covered:'var(--acc)',historical:'var(--none)',unknown:'var(--warn)',inconclusive:'var(--warn)',satisfied:'var(--ok)',partial:'var(--warn)',failed:'var(--bad)',unobserved:'var(--none)',invalidated:'var(--inv)'};
let S=null;
async function tick(){try{S=await (await fetch('/api/state',{cache:'no-store'})).json();render(S);}catch(e){console.error(e)}}
function ago(ms){if(!ms)return'';const d=(S.now-ms)/1000;return d<60?Math.round(d)+'s':Math.round(d/60)+'m'}
function render(s){
  $('#st-traces').textContent=s.stats?.traces||0;$('#st-spans').textContent=s.stats?.spans||0;$('#st-gaps').textContent=s.stats?.gaps||0;$('#st-mism').textContent=s.stats?.mismatches||0;
  if(document.activeElement!==$('#thr'))$('#thr').value=s.policy.threshold;
 if(document.activeElement!==$('#mode'))$('#mode').value=s.policy.require_confidence?'confidence':'coverage';
 renderCapabilities(s);
  renderInstances(s);renderGraph(s);renderMatrix(s);renderObligations(s);renderPaths(s);
  $('#events').innerHTML=(s.events||[]).map(e=>'<div><span class="t">'+new Date(e.ts).toLocaleTimeString()+'</span><span class="k-'+e.kind+'">'+esc(e.kind)+'</span> '+esc(e.msg)+'</div>').join('')||'<div class="empty">no events yet</div>';
  $('#recent').innerHTML='<table><tr><th>when</th><th>path</th><th>scenario</th><th>flags</th><th>tenant</th><th>src</th><th>outcome</th><th class="num">ms</th><th>credit</th></tr>'+(s.recent||[]).map(r=>'<tr class="'+(r.credited?'':'dim')+'"><td>'+ago(r.ts)+'</td><td>'+esc(r.path)+'</td><td>'+esc(r.scenario)+'</td><td>'+esc(r.flags)+'</td><td>'+esc(r.tenant)+(r.eligible?' <span class="chip candidate">canary</span>':'')+'</td><td>'+esc(r.source)+'</td><td class="'+(r.failed?'s-failed':'s-satisfied')+'">'+esc(r.failed?'error':r.outcome)+'</td><td class="num">'+r.lat_ms.toFixed(0)+'</td><td title="'+esc(r.note||'')+'">'+(r.credited?'✓':'<span class="s-failed">✗</span> '+esc(r.note||''))+'</td></tr>').join('')+'</table>';
}
function renderInstances(s){
  const ob={};(s.obligations||[]).forEach(o=>ob[o.instance]=o);
  $('#instances').innerHTML=s.services.map(svc=>'<div class="col"><h3>'+esc(svc)+'</h3>'+s.instances.filter(i=>i.service===svc).map(i=>{
    const o=ob[i.id];const st=o?'<span class="chip '+o.status+'">'+o.status.toUpperCase()+(o.rows.length?' '+o.satisfied+'/'+o.rows.length:'')+'</span>':'';
    const btn=(i.role==='candidate'||i.role==='draining')?'<button class="danger" style="margin-left:auto" data-retire="'+esc(i.id)+'">retire</button>':'';
    const cfg=[i.fail_rate?'fail_rate='+i.fail_rate:'',i.latency_ms?'latency='+i.latency_ms+'ms':'',i.contract!=='v1'?'contract='+i.contract:''].filter(Boolean).join(' · ');
    return '<div class="card"><div class="top"><span class="id">'+esc(i.id)+'</span><span class="chip '+i.role+'">'+i.role+'</span>'+st+btn+'</div><div class="meta">v'+esc(i.version)+' · node_key <code>'+esc(i.node_key.slice(0,8))+'</code> · routed '+i.routes+'× · seen '+ago(i.last_seen)+' ago'+(cfg?'<br><span class="s-partial">'+esc(cfg)+'</span>':'')+'</div></div>'}).join('')+'</div>').join('');
}
function renderGraph(s){
  const svcs=s.services, colW=240, nodeW=150, nodeH=44, gapY=26, padX=40;
  const cols=svcs.map(sv=>s.nodes.filter(n=>n.service===sv));
  const maxN=Math.max(1,...cols.map(c=>c.length));
  const H=maxN*(nodeH+gapY)+30, W=padX*2+(svcs.length-1)*colW+nodeW;
  const pos={};cols.forEach((col,i)=>{const total=col.length*(nodeH+gapY)-gapY;col.forEach((n,j)=>{pos[n.id]={x:padX+i*colW,y:(H-total)/2+j*(nodeH+gapY)}})});
  const maxC=Math.max(1,...s.edges.map(e=>e.count));
  let out='';
  s.edges.forEach(e=>{const a=pos[e.from],b=pos[e.to];if(!a||!b)return;const x1=a.x+nodeW,y1=a.y+nodeH/2,x2=b.x,y2=b.y+nodeH/2,mx=(x1+x2)/2;
    const w=e.count?1.5+6*Math.log(1+e.count)/Math.log(1+maxC):1.2;const col=COLOR[e.status]||COLOR.unobserved;
    out+='<path d="M'+x1+','+y1+' C'+mx+','+y1+' '+mx+','+y2+' '+x2+','+y2+'" fill="none" stroke="'+col+'" stroke-width="'+w.toFixed(1)+'"'+(e.status==='unobserved'?' stroke-dasharray="5,5"':'')+' opacity=".9"><title>'+esc(s.nodes.find(n=>n.id===e.from)?.label)+' → '+esc(s.nodes.find(n=>n.id===e.to)?.label)+': '+e.count+' traces, '+e.errors+' errors, '+e.rows+' scenario rows ('+e.status+')</title></path>';
    if(e.count){out+='<rect x="'+(mx-22)+'" y="'+((y1+y2)/2-9)+'" width="44" height="18" rx="4" fill="#0d1117" stroke="'+col+'" stroke-width="1"/><text x="'+mx+'" y="'+((y1+y2)/2+4)+'" text-anchor="middle" style="font-size:11px">'+e.count+'</text>'}
    else{out+='<text x="'+mx+'" y="'+((y1+y2)/2-6)+'" text-anchor="middle" style="font-size:11px;fill:var(--none)">◌</text>'}
  });
  s.nodes.forEach(n=>{const p=pos[n.id];if(!p)return;const stroke={baseline:'var(--blue)',candidate:'var(--warn)',draining:'var(--inv)'}[n.role]||'var(--none)';
    out+='<g><rect x="'+p.x+'" y="'+p.y+'" width="'+nodeW+'" height="'+nodeH+'" rx="8" fill="#0d1117" stroke="'+stroke+'" stroke-width="1.5"'+(n.alive?'':' stroke-dasharray="4,3" opacity=".6"')+'/><text x="'+(p.x+10)+'" y="'+(p.y+18)+'" style="font-weight:600">'+esc(n.label)+'</text><text x="'+(p.x+10)+'" y="'+(p.y+34)+'" style="font-size:10px;fill:var(--mut)">'+esc(n.role)+' · '+esc(n.node_key.slice(0,8))+'</text></g>';
  });
  $('#graph').innerHTML='<svg viewBox="0 0 '+W+' '+H+'" width="100%" style="max-width:'+W+'px;display:block">'+out+'</svg>';
}
function renderMatrix(s){
  const m=s.matrix;if(!m||!m.rows){$('#matrix').innerHTML='<div class="empty">waiting for instances…</div>';return}
  $('#matrix-hint').textContent='· diagnostic only; promotion uses full paths and all checks above';
  let h='<table class="matrix"><tr><th>'+esc(m.row_service)+' \\ '+esc(m.col_service)+'</th>'+m.cols.map(c=>'<th>'+esc(m.labels[c])+'</th>').join('')+'</tr>';
  m.rows.forEach(r=>{h+='<tr><td class="rowhead">'+esc(m.labels[r])+'</td>'+m.cols.map(c=>{const cl=m.cells[r+'|'+c]||{status:'exempt'};return '<td class="cell s-'+cl.status+'" title="'+cl.status+': '+cl.count+' traces, '+cl.errors+' errors, '+cl.full+'/'+cl.rows+' rows at threshold">'+SYM[cl.status]+'<div style="font-size:10px;color:var(--mut)">'+(cl.count?cl.count+' · '+cl.full+'/'+cl.rows:'')+'</div></td>'}).join('')+'</tr>'});
  $('#matrix').innerHTML=h+'</table>';
}
function percent(x){return (100*x).toFixed(1)+'%'}
function renderCapabilities(s){
 const mode=s.policy.require_confidence?'Confidence gate':'Demo coverage gate';
 $('#capabilities').innerHTML='<div class="evidence-grid"><div><b>Coverage</b><p>Unique complete requests on exact versioned paths. Minimum '+s.policy.threshold+' fresh requests per requirement.</p></div><div><b>Correctness</b><p>Explicit todo response assertions. A 200 response can fail. Durable writes and cross-request invariants are not checked.</p></div><div><b>Freshness & performance</b><p>Rolling '+s.policy.evidence_ttl_s+'s window. Mean latency compared with the matching fresh baseline.</p></div><div><b>'+mode+'</b><p>'+(s.policy.require_confidence?'The 95% error upper bound must satisfy the error budget.':'Sample minimum controls promotion; the confidence bound is displayed but does not gate.')+' The bound assumes independent representative requests.</p></div></div><div class="decision">'+(s.ledger_error?'<b class="s-failed">'+esc(s.ledger_error)+'</b>':'Scope: synchronous linear paths with retries. Concurrent candidates are tested separately. Fan-out, async work, and automatic rollback are not implemented.')+'</div>';
}
function renderObligations(s){
 const obs=s.obligations||[];
 if(!obs.length){$('#obligations').innerHTML='<div class="empty">No candidates. Deploy a version to see its full-path requirements.</div>';return}
 $('#obligations').innerHTML=obs.map(o=>'<div class="ob"><div class="head"><b>'+esc(o.instance)+'</b><span class="chip '+o.status+'">'+o.status.toUpperCase()+'</span><span class="stat">'+o.satisfied+'/'+o.rows.length+' requirements · baseline '+esc(o.baseline)+(s.active_candidate===o.instance?' · receiving experiment traffic':'')+'</span></div>'+(o.reasons.length?'<div class="decision">'+o.reasons.map(esc).join(' · ')+'</div>':'')+(o.rows.length?'<div class="scroll"><table><tr><th>required full path</th><th>scenario / flag</th><th>coverage</th><th>correctness</th><th>freshness</th><th>error upper 95%</th><th>latency / baseline</th><th>decision</th></tr>'+o.rows.map(r=>{const e=r.evidence;return '<tr><td>'+esc(r.label)+'</td><td>'+esc(r.scenario)+'<br><span class="stat">'+esc(r.flags)+'</span></td><td>'+r.count+'/'+r.threshold+'<br><span class="stat">'+esc(e.coverage)+'</span></td><td>'+esc(e.correctness.replaceAll('_',' '))+'<br><span class="stat">'+r.errors+' errors</span></td><td>'+esc(e.freshness)+'</td><td>'+percent(e.error_upper_95)+'<br><span class="stat">'+(e.confidence==='insufficient'?'insufficient':'supported under assumptions')+'</span></td><td>'+e.avg_ms.toFixed(0)+' / '+e.baseline_avg_ms.toFixed(0)+' ms<br><span class="stat">'+esc(e.performance.replaceAll('_',' '))+'</span></td><td class="s-'+r.status+'">'+(SYM[r.status]||'?')+' '+esc(r.status)+'<div class="reason">'+e.reasons.map(esc).join('; ')+'</div></td></tr>'}).join('')+'</table></div>':'<div class="empty">Waiting for successful baseline observations.</div>')+'</div>').join('');
}
function renderPaths(s){
 const ps=s.paths||[];if(!ps.length){$('#paths').innerHTML='<div class="empty">No complete observations yet.</div>';return}
 $('#paths').innerHTML='<div class="scroll"><table><tr><th>full versioned path</th><th>scenario</th><th>flags</th><th>fresh / lifetime</th><th>errors</th><th>response checks</th><th>error upper 95%</th><th>mean ms</th><th>state</th></tr>'+ps.map(p=>'<tr><td>'+esc(p.label)+'</td><td>'+esc(p.scenario)+'</td><td>'+esc(p.flags)+'</td><td>'+p.count+' / '+p.total+'</td><td>'+p.errors+'</td><td>'+esc(p.evidence.correctness.replaceAll('_',' '))+'</td><td>'+percent(p.evidence.error_upper_95)+'</td><td>'+p.avg_ms.toFixed(0)+'</td><td class="s-'+p.status+'">'+esc(p.status)+'</td></tr>').join('')+'</table></div>';
}
async function retire(id){if(!confirm('Retire '+id+'? It will exit on its next heartbeat and its evidence will remain as historical observations.'))return;const r=await fetch('/api/retire?id='+encodeURIComponent(id),{method:'POST'});if(!r.ok)alert(await r.text());tick()}
async function reset(){if(!confirm('Clear all evidence?'))return;await fetch('/api/reset',{method:'POST'});tick()}
async function setThr(){await fetch('/api/policy?threshold='+$('#thr').value+'&mode='+$('#mode').value,{method:'POST'});tick()}
document.addEventListener('click',e=>{const b=e.target.closest('[data-retire]');if(b)retire(b.dataset.retire)});
async function libraryTick(){
 try{
  const r=await fetch('/api/library',{cache:'no-store'});if(!r.ok)throw Error('Evidence service unavailable');
  const s=await r.json();
  if(!s.enabled){$('#library').innerHTML='<div class="empty">Evidence export is disabled. Start the full demo compose stack to exercise the libraries.</div>';return}
  const x=s.export||{},reports=s.reports||[];
  let out='<div class="decision">Delivered '+(x.Delivered||0)+' · export failures '+(x.Failed||0)+' · dropped '+(x.Dropped||0)+'. '+esc(s.error||'Source-separated observations; fixed service coverage minimum of six. Demo promotion settings above apply to the rollout controller.')+'</div>';
  if((x.Failed||0)+(x.Dropped||0)>0)out+='<p class="s-unknown">Some observations were lost. These rows describe received evidence only.</p>';
  out+='<div class="scroll"><table><tr><th>observed path</th><th>scenario / version</th><th>flag</th><th>source / environment</th><th>fresh / lifetime</th><th>checks</th><th>error upper 95%</th><th>assessment</th></tr>';
  out+=reports.map(r=>'<tr><td>'+esc(r.series.path.map(p=>p.service+'@'+p.version).join(' → '))+'</td><td>'+esc(r.series.scenario.name+' / '+r.series.scenario.version)+'</td><td>'+esc(Object.entries(r.series.flags).map(([k,v])=>k+'='+v).join(', '))+'</td><td>'+esc(r.series.source+' / '+r.series.environment)+'</td><td>'+r.window.count+' / '+r.window.total+'</td><td>'+esc(r.assessment.correctness.replaceAll('_',' '))+'</td><td>'+percent(r.assessment.error_upper_95)+'</td><td class="s-'+esc(r.assessment.status)+'">'+esc(r.assessment.status)+'</td></tr>').join('');
  $('#library').innerHTML=out+'</table></div><p class="stat">Application-owned checks; no automatic promotion or baseline latency comparison in this observed-series view. Reset evidence above clears the demo rollout ledger; library history has its own retention.</p>';
 }catch(e){$('#library').innerHTML='<div class="s-unknown">Evidence service unavailable; library evidence is unknown.</div>'}
 finally{setTimeout(libraryTick,3000)}
}
libraryTick();tick();setInterval(tick,1500);
</script></body></html>`
