package main

import "net/http"

// handleApp serves the todo front end. It is deliberately plain JavaScript:
// every click goes through the ingress (/app/api/…), which starts the trace,
// decides eligibility from the tenant, and routes to a versioned api instance.
func (c *controller) handleApp(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(appHTML))
}

const appHTML = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Todos · ECG demo</title>
<style>
:root{--bg:#0d1117;--panel:#161b22;--line:#30363d;--fg:#e6edf3;--mut:#8b949e;--ok:#3fb950;--warn:#d29922;--bad:#f85149;--acc:#58a6ff}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--fg);font:15px/1.45 ui-sans-serif,system-ui,-apple-system,Segoe UI,Roboto,sans-serif}
.wrap{max-width:980px;margin:0 auto;padding:24px 16px;display:grid;grid-template-columns:1.1fr 1fr;gap:18px}
@media (max-width:780px){.wrap{grid-template-columns:1fr}}
.panel{background:var(--panel);border:1px solid var(--line);border-radius:12px;padding:16px}
h1{font-size:18px;margin:0 0 12px}h2{font-size:12px;color:var(--mut);text-transform:uppercase;letter-spacing:.06em;margin:0 0 10px}
.row{display:flex;gap:8px;align-items:center;margin-bottom:10px;flex-wrap:wrap}
input[type=text],select{font:inherit;background:#0d1117;color:var(--fg);border:1px solid var(--line);border-radius:8px;padding:8px 10px}
input[type=text]{flex:1;min-width:160px}
button{font:inherit;background:#21262d;color:var(--fg);border:1px solid var(--line);border-radius:8px;padding:7px 12px;cursor:pointer}button:hover{border-color:var(--acc)}
button.primary{background:#1f6feb;border-color:#1f6feb}button.ghost{background:transparent}button.small{padding:3px 8px;font-size:12px}
label.chk{color:var(--mut);font-size:13px;display:flex;gap:6px;align-items:center}
ul{list-style:none;margin:0;padding:0}li{display:flex;align-items:center;gap:10px;padding:8px 6px;border-bottom:1px solid var(--line)}
li.done span.t{text-decoration:line-through;color:var(--mut)}li span.t{flex:1}li .pri{font-size:11px;color:var(--warn)}
.chip{display:inline-block;padding:1px 8px;border-radius:999px;font-size:11px;font-weight:600;border:1px solid var(--line);color:var(--mut)}
.chip.canary{color:#e3b341;border-color:#d2992266;background:#d2992233}.chip.ok{color:var(--ok)}.chip.bad{color:var(--bad)}.chip.warn{color:var(--warn)}
.path{font:13px ui-monospace,SFMono-Regular,Menlo,monospace;color:var(--acc)}
.kv{display:grid;grid-template-columns:auto 1fr;gap:4px 12px;font-size:13px}.kv b{color:var(--mut);font-weight:500}
.log{font-size:12px;max-height:300px;overflow:auto}.log div{padding:5px 0;border-bottom:1px solid var(--line);display:flex;gap:8px;align-items:center}.log .m{color:var(--mut);min-width:56px}
.msg{min-height:20px;font-size:13px;margin-top:6px}.msg.bad{color:var(--bad)}.msg.ok{color:var(--ok)}
a{color:var(--acc);text-decoration:none}
</style></head><body><div class="wrap">
<div class="panel">
  <h1>Todos <span class="chip" id="tenant-chip">—</span></h1>
  <div class="row">
    <select id="tenant" onchange="load()"></select>
    <label class="chk"><input type="checkbox" id="chaos"> slow store (times out → retry path)</label>
    <a href="/" target="_blank" style="margin-left:auto;font-size:13px">dashboard ↗</a>
  </div>
  <form class="row" onsubmit="add(event)">
    <input type="text" id="title" placeholder="What needs doing? (prefix with ! for high priority; submit empty to exercise the rejected path)" autocomplete="off">
    <button class="primary" type="submit">Add</button>
  </form>
  <div class="msg" id="msg"></div>
  <ul id="list"></ul>
</div>
<div class="panel">
  <h2>Last request · which versioned path served it</h2>
  <div class="kv" id="last"><b>path</b><span class="path">—</span></div>
  <h2 style="margin-top:18px">Request log</h2>
  <div class="log" id="log"></div>
</div>
</div>
<script>
const TENANTS=['acme','globex','initech','umbrella','hooli','stark'];
const $=s=>document.querySelector(s);const esc=s=>String(s??'').replace(/[&<>"]/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[c]));
$('#tenant').innerHTML=TENANTS.map(t=>'<option>'+t+'</option>').join('');
$('#tenant').value=localStorage.getItem('ecg.tenant')||'acme';
const logs=[];
async function api(method,path,body){
  const t=$('#tenant').value;localStorage.setItem('ecg.tenant',t);
  const h={'Content-Type':'application/json','X-Tenant':t};if($('#chaos').checked)h['X-Ecg-Chaos']='slow-store';
  const t0=performance.now();
  const r=await fetch('/app/api'+path,{method,headers:h,body:body?JSON.stringify(body):undefined});
  const ms=performance.now()-t0;let j={};try{j=await r.json()}catch(e){}
  const e=j.ecg||{};
  $('#tenant-chip').textContent=t+(e.eligible?' · canary':'');$('#tenant-chip').className='chip'+(e.eligible?' canary':'');
  $('#last').innerHTML='<b>path</b><span class="path">'+esc(e.path||'—')+'</span><b>status</b><span>'+r.status+' <span class="chip '+(r.status>=500?'bad':r.status>=400?'warn':'ok')+'">'+esc(e.outcome||'')+'</span>'+(e.retries?' <span class="chip warn">'+e.retries+' retry</span>':'')+'</span><b>scenario</b><span>'+esc(e.scenario)+'</span><b>flags</b><span>'+esc(e.flags)+'</span><b>eligible</b><span>'+(e.eligible?'yes — may be routed to an unverified candidate':'no — baseline only')+'</span><b>routed</b><span>'+esc(r.headers.get('X-Ecg-Route')||'')+'</span><b>latency</b><span>'+ms.toFixed(0)+' ms</span>';
  logs.unshift('<div><span class="m">'+method+' '+esc(path)+'</span><span class="chip '+(r.status>=500?'bad':r.status>=400?'warn':'ok')+'">'+r.status+'</span><span class="path">'+esc(e.path||'')+'</span><span style="color:var(--mut)">'+esc(e.scenario||'')+' · '+ms.toFixed(0)+'ms</span></div>');
  logs.length=Math.min(logs.length,15);$('#log').innerHTML=logs.join('');
  return {status:r.status,body:j};
}
function flash(txt,cls){const m=$('#msg');m.textContent=txt;m.className='msg '+(cls||'');}
async function load(){const {body}=await api('GET','/todos');renderList(body.todos||[]);}
function renderList(ts){
  $('#list').innerHTML=ts.map(t=>'<li class="'+(t.done?'done':'')+'"><input type="checkbox" '+(t.done?'checked disabled':'')+' onchange="toggle('+t.id+')"><span class="t">'+esc(t.title)+'</span>'+(t.priority==='high'?'<span class="pri">high</span>':'')+'<button class="ghost small" onclick="del('+t.id+')">✕</button></li>').join('')||'<li style="color:var(--mut)">nothing yet</li>';
}
async function add(ev){ev.preventDefault();const title=$('#title').value;const {status,body}=await api('POST','/todos',{title});
  if(status>=500){flash('server error: '+body.error,'bad')}else if(status===422){flash('rejected: '+body.error,'bad')}else{flash('added','ok');$('#title').value=''}
  load();}
async function toggle(id){await api('POST','/todos/'+id+'/complete');load()}
async function del(id){await api('DELETE','/todos/'+id);load()}
load();
</script></body></html>`
