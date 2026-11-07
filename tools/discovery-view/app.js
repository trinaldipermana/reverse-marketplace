const $=id=>document.getElementById(id);
const esc=v=>String(v??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
const money=v=>new Intl.NumberFormat('id-ID',{style:'currency',currency:'IDR',maximumFractionDigits:0}).format(v||0);
const stages=[['Inventory','What can we sell?','Discovery starts with the seller’s available items.'],['Generate search queries','What should we search for?','AI turns the inventory into marketplace search terms.'],['Search posts','Who is looking to buy?','Search finds posts and removes duplicates.'],['Match items','Which item fits?','AI suggests a match. Code checks the seller’s rules.'],['Save lead','Lead saved','']];
const anchors=['inventory','queries','posts','checks','leads'];
const MIN_STEP_MS=0;
let sellerId='seller_demo';
let data=null,busy=false,selectedRun='',walking=false,timer=null,refreshing=false,follow=false,priorRun=null,liveTimer=null,stepEnteredAt=0,step=Math.max(0,anchors.indexOf(location.hash.slice(1)));
function stop(){clearInterval(timer);timer=null;walking=false;$('replay').textContent='▶ Walk through';}
function stopLive(){clearInterval(liveTimer);liveTimer=null;follow=false;}
function advanceLive(){
 if(!follow||!data)return;
 const target=liveStep(data);
 if(target>step&&Date.now()-stepEnteredAt>=(step===0?0:MIN_STEP_MS)){step++;stepEnteredAt=Date.now();render();}
 if(!busy&&step>=target)stopLive();
}
function select(index){stop();stopLive();step=Math.max(0,Math.min(stages.length-1,index));history.replaceState(null,'','#'+anchors[step]);render();}
function liveStep(d){
 if(!d.run||d.run.id===priorRun)return 0;
 if(!d.queries.length)return 1;
 if(!d.posts.length)return 2;
 const evaluated=d.posts.filter(p=>['completed','failed'].includes(p.matchingStatus)).length;
 if(evaluated<d.posts.length)return 3;
 if(!d.leads.length)return 3;
 if(d.leads.some(l=>!l.notification||l.notification.status!=='accepted'))return 4;
 return 4;
}
function productImage(item){
 const url=item?.attributes?.image_url||'';
 const safe=/^https?:\/\//i.test(url)||/^\/(?!\/)/.test(url);
 return `<div class="product-picture">${safe?`<img src="${esc(url)}" alt="${esc(item.name)} illustration" loading="lazy" onerror="this.hidden=true;this.nextElementSibling.hidden=false"><span hidden>Image unavailable</span>`:'<span>No image yet</span>'}</div>`;
}
function render(){
 const d=data;if(!d)return;
 $('seller-info').textContent='Seller: '+sellerId;
 const items=d.inventory.filter(i=>i.active&&i.available>0),matches=d.matches.filter(m=>m.status==='selected'),match=matches[0],post=match?d.posts.find(p=>p.id===match.postId):d.posts.find(p=>p.intent?.isBuying)||d.posts[0],item=match?d.inventory.find(i=>i.id===match.itemId):null;
 const lead=d.leads.find(l=>l.id===match?.leadId)||d.leads[0],accepted=d.leads.filter(l=>l.notification?.status==='accepted').length,probe=(d.backend.runtimeLabel||'').toLowerCase().includes('probe');
 const counts=[items.length,d.queries.length,d.posts.length,matches.length,d.leads.length,accepted];
 $('steps').innerHTML=stages.map((s,i)=>`<button class="step-button ${i===step?'active':''}" data-step="${i}" aria-current="${i===step?'step':'false'}"><span class="number">${i+1}</span><span>${esc(s[0])}</span></button>`).join('');
 $('steps').querySelectorAll('button').forEach(b=>b.onclick=()=>select(Number(b.dataset.step)));
 $('step-label').textContent=`STEP ${step+1} OF ${stages.length}`;$('step-title').textContent=stages[step][1];$('step-description').textContent=stages[step][2];$('step-description').hidden=true;$('previous').disabled=step===0;$('next').disabled=step===stages.length-1;
 const empty=text=>`<p class="empty">${esc(text)}</p>`;
 const views=[
 ()=>items.length?`<div class="inventory">${items.slice(0,3).map(i=>`<article class="item product-card">${productImage(i)}<div class="product-copy"><strong>${esc(i.name)}</strong><span>${esc(money(i.priceIDR))}</span><small>${i.available} available</small></div></article>`).join('')}</div>`:empty('No available inventory.'),
 ()=>d.queries.length?`<div class="search-terms">${d.queries.map(q=>`<span>${esc(q.text)}</span>`).join('')}</div><p class="caption">${d.queries.length} searches generated from inventory</p>`:empty('No search queries saved yet.'),
 ()=>post?`<article class="buyer-post"><div class="post-header"><div class="post-avatar" aria-hidden="true">${esc((post.buyerId||'B').slice(0,1).toUpperCase())}</div><div><strong>${esc(post.buyerId||'Buyer')}</strong><span>${esc(post.platform)} · Marketplace post</span></div></div><div class="post-body">${esc(post.text)}</div><div class="post-footer"><span>⌖ ${esc(post.sourceCity||'Location not provided')}</span><span>${post.intent?.isBuying?'Looking to buy':'Marketplace post'}</span></div></article>`:empty('No buyer posts found yet.'),
 ()=>item?`<div class="match-cards"><article class="buyer-post"><div class="post-header"><div class="post-avatar" aria-hidden="true">${esc((post?.buyerId||'B').slice(0,1).toUpperCase())}</div><div><strong>${esc(post?.buyerId||'Buyer')}</strong><span>Buying request · ${esc(post?.sourceCity||'Marketplace')}</span></div></div><div class="post-body">${esc(post?.text||post?.intent?.itemQuery||'A matching item')}</div>${post?.intent?.budgetIDR?`<div class="post-footer">Budget ${esc(money(post.intent.budgetIDR))}</div>`:''}</article><span class="match-connector" aria-label="matches">→</span><article class="product-card match-product">${productImage(item)}<div class="product-copy"><span class="match-tag">Matched product</span><strong>${esc(item.name)}</strong><span>${esc(money(item.priceIDR))}</span><small>${item.available} available</small></div></article></div>`:empty(d.run?'No eligible match saved for this run.':'Matches appear after discovery.'),
 ()=>lead?`<div class="outcome"><div class="result-icon">✓</div><div><h3>${d.leads.length} ${d.leads.length===1?'lead':'leads'} saved</h3><p>${esc(d.inventory.find(i=>i.id===lead.itemId)?.name||'Matched item')}</p></div></div>`:empty('No lead saved yet.')
 ];
 $('content').innerHTML=views[step]();
 $('discover').disabled=busy||!d.backend.reachable;$('discover').textContent=busy?'Discovering…':'Discover buyers ↗';$('replay').disabled=busy||!d.run;
}
async function refresh(){if(refreshing)return;refreshing=true;try{const q=new URLSearchParams({seller:sellerId});if(selectedRun)q.set('run',selectedRun);const r=await fetch('/inspect/state?'+q,{cache:'no-store'});const body=await r.json();if(!r.ok)throw Error(body.error||'Could not load discovery');data=body;if(follow)advanceLive();render();}catch(e){$('error').hidden=false;$('error').textContent=e.message;}finally{refreshing=false;}}
$('previous').onclick=()=>select(step-1);$('next').onclick=()=>select(step+1);
$('replay').onclick=()=>{if(walking){stop();render();return;}stopLive();walking=true;step=1;$('replay').textContent='Ⅱ Pause';render();timer=setInterval(()=>{if(step===stages.length-1){stop();render();return;}step++;render();},MIN_STEP_MS);};
$('discover').onclick=async()=>{stop();stopLive();busy=true;follow=true;stepEnteredAt=Date.now();liveTimer=setInterval(advanceLive,100);priorRun=data?.run?.id||null;step=0;selectedRun='';$('error').hidden=true;render();const poll=setInterval(refresh,800);try{const r=await fetch('/inspect/discover',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({sellerId:sellerId})});const body=await r.json();if(!r.ok)throw Error(body.error?.message||body.error||'Discovery failed');selectedRun=body.runId||'';}catch(e){$('error').hidden=false;$('error').textContent=e.message;}finally{clearInterval(poll);await refresh();busy=false;advanceLive();render();}};
document.addEventListener('keydown',e=>{if(['INPUT','SELECT','TEXTAREA'].includes(e.target.tagName))return;if(e.key==='ArrowRight')select(step+1);if(e.key==='ArrowLeft')select(step-1);});window.addEventListener('beforeunload',()=>{stop();stopLive();});refresh();
