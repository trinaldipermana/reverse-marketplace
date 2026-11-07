const $=id=>document.getElementById(id);
const esc=v=>String(v??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
const money=v=>new Intl.NumberFormat('id-ID',{style:'currency',currency:'IDR',maximumFractionDigits:0}).format(v||0);
const titles=['Inventory','Generate search queries','Search posts','Match items','Save lead'];
const HOLD=0;
let data=null,position=5,mode='saved',busy=false,previousRun=null,selectedRun='',entered=0,ticker=null,poll=null,fetching=false,generation=0;
function image(item,cls=''){const raw=item?.attributes?.image_url||'';return (/^https?:\/\//i.test(raw)||/^\/(?!\/)/.test(raw))?`<img class="${cls}" src="${esc(raw)}" alt="${esc(item.name)} illustration" onerror="this.hidden=true">`:'';}
function visibleData(){if(mode==='live'&&data?.run?.id===previousRun)return {...data,run:null,queries:[],posts:[],matches:[],leads:[]};return data;}
function target(d){
 if(!d?.run)return 1;
 if(d.run.status!=='running')return 5;
 if(!d.queries.length)return 1;
 if(!d.posts.length)return 2;
 if(d.posts.some(p=>!['completed','failed'].includes(p.matchingStatus)))return 3;
 return d.leads.length?4:3;
}
function finishTimers(){clearInterval(ticker);ticker=null;clearInterval(poll);poll=null;}
function render(){
 const d=visibleData();if(!d)return;
 const scrollPositions=Array.from(document.querySelectorAll('.stage-body'),el=>el.scrollTop);
 const items=d.inventory.filter(i=>i.active&&i.available>0),matches=d.matches.filter(m=>m.status==='selected');
 const contents=[
 ()=>items.map(i=>`<div class="small-product">${image(i)}<div><strong>${esc(i.name)}</strong><span>${esc(money(i.priceIDR))}</span></div></div>`).join('')||'<p class="waiting">No available items</p>',
 ()=>d.queries.length?d.queries.map(q=>`<div class="term">${esc(q.text)}</div>`).join(''):'<p class="waiting">No queries saved</p>',
 ()=>d.posts.length?d.posts.map(post=>`<article class="post"><div class="post-author"><span class="avatar">${esc((post.buyerId||'B')[0].toUpperCase())}</span>${esc(post.buyerId||'Buyer')}</div><p>${esc(post.text)}</p><div class="post-meta"><span>⌖ ${esc(post.sourceCity||'Location unknown')}</span><span>${post.matchingStatus==='completed'?(post.intent?.isBuying?'Buying request':'Not a buying request'):'Not evaluated yet'}</span></div></article>`).join(''):'<p class="waiting">No posts found</p>',
 ()=>matches.length?matches.map(match=>{const item=d.inventory.find(i=>i.id===match.itemId),post=d.posts.find(p=>p.id===match.postId);return `<article class="match-pair"><div class="paired-post"><div class="post-author"><span class="avatar">${esc((post?.buyerId||'B')[0].toUpperCase())}</span>${esc(post?.buyerId||'Buyer')}</div><p>${esc(post?.text||'Original post unavailable')}</p></div><div class="pair-arrow" aria-label="matched to">↓</div><div class="paired-item">${image(item)}<div><strong>${esc(item?.name||match.itemId)}</strong>${item?`<span>${esc(money(item.priceIDR))}</span>`:''}</div></div></article>`;}).join(''):'<p class="waiting">No eligible match</p>',

 ()=>d.leads.length?`<div class="lead-mark">✓</div><div class="lead-total">${d.leads.length} ${d.leads.length===1?'lead':'leads'} saved</div>${d.leads.map(lead=>`<div class="saved-lead"><strong>${esc(d.inventory.find(i=>i.id===lead.itemId)?.name||lead.itemId)}</strong><span>${esc(lead.state)}</span></div>`).join('')}`:'<div class="lead-mark neutral">—</div><div class="lead-total">No lead</div>'
 ];
 const present=[items.length,d.queries.length,d.posts.length,matches.length,d.leads.length];
 const totals=[`${items.length} available`,`${d.queries.length} queries`,`${d.posts.length} posts found`,`${d.matches.filter(m=>m.status==='selected').length} ${d.matches.filter(m=>m.status==='selected').length===1?'match':'matches'}`,`${d.leads.length} ${d.leads.length===1?'lead':'leads'}`];
 $('board').innerHTML=titles.map((title,i)=>{
  const active=i===position,done=i<position,shown=done||active&&present[i];
  let state=done?(present[i]?'✓ '+totals[i]:i===4?'No lead created':'No results'):active?(present[i]?totals[i]:'Working…'):'Waiting';
  if(mode==='paused'&&active)state='Replay paused';
  return `<article class="stage ${active?'active':''} ${done?'done':''}" aria-label="Step ${i+1}: ${esc(title)}${active?', current step':''}"><div class="stage-head"><div class="stage-title"><span class="number">${done&&present[i]?'✓':i+1}</span><h2>${esc(title)}</h2></div><span class="state">${esc(state)}</span></div><div class="stage-body" tabindex="0" role="region" aria-label="${esc(title)} results">${shown?contents[i]():`<p class="waiting">${active?'Waiting for results…':'Waiting for previous step'}</p>`}</div></article>`;
 }).join('');
 document.querySelectorAll('.stage-body').forEach((el,i)=>{el.scrollTop=scrollPositions[i]||0;});
 $('mode').textContent=mode==='replay'?'Replay · saved results':mode==='paused'?'Replay paused · saved results':mode==='live'?(busy?'Live discovery':'Presenting completed run'):d.run?`Saved run · ${d.run.status}`:'Ready to discover';
 $('progress').textContent=position<5?`Step ${position+1} of 5`:(d.run?'All steps shown':'');
 $('replay').textContent=mode==='replay'?'Ⅱ Pause':'▶ Replay';$('replay').disabled=!data.run||busy||mode==='live';
 $('discover').disabled=busy||mode==='live'||!data.backend.reachable;$('discover').textContent=busy?'Discovering…':'Discover buyers ↗';
}
async function refresh(){if(fetching)return;fetching=true;const epoch=generation,run=selectedRun;try{const q=new URLSearchParams({seller:'seller_demo'});if(run)q.set('run',run);const response=await fetch('/inspect/state?'+q,{cache:'no-store'}),value=await response.json();if(!response.ok)throw Error(value.error?.message||'Could not read discovery');if(epoch!==generation||run!==selectedRun)return;data=value;render();}catch(e){$('error').hidden=false;$('error').textContent=e.message;}finally{fetching=false;}}
function advance(){const d=visibleData();const end=mode==='replay'?5:target(d);if(position<end&&Date.now()-entered>=(position===0?0:HOLD)){position++;entered=Date.now();render();}if(position===5){clearInterval(ticker);ticker=null;mode='saved';render();}}
$('replay').onclick=()=>{if(mode==='replay'){clearInterval(ticker);ticker=null;mode='paused';render();return;}finishTimers();mode='replay';position=1;entered=Date.now();ticker=setInterval(advance,100);render();};
$('discover').onclick=async()=>{
 if(busy||mode==='live')return;
 finishTimers();generation++;previousRun=data?.run?.id||null;selectedRun='';busy=true;mode='live';position=1;entered=Date.now();$('error').hidden=true;render();ticker=setInterval(advance,100);poll=setInterval(refresh,800);
 try{const response=await fetch('/inspect/discover',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({sellerId:'seller_demo'})}),result=await response.json();if(!response.ok)throw Error(result.error?.message||result.error||'Discovery failed');selectedRun=result.runId||'';}
 catch(e){$('error').hidden=false;$('error').textContent=e.message;clearInterval(ticker);ticker=null;mode='saved';position=5;}
 finally{clearInterval(poll);poll=null;busy=false;while(fetching)await new Promise(resolve=>setTimeout(resolve,50));await refresh();render();}
};
window.addEventListener('beforeunload',finishTimers);refresh();
