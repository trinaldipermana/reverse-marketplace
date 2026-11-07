import { useEffect, useMemo, useRef, useState } from 'react';
import Avatar from '../../components/Avatar.jsx';
import {
  demoBuyerRequest,
  discoverHackathonBuyers,
  hackathonSellerId,
  makeHackathonPost,
  readHackathonLead,
  readHackathonThread,
  saveHackathonPost,
  sendHackathonReply,
} from '../../api/hackathon.js';
import './hackathon-demo.css';

function clockTime(value) {
  if (!value) return 'Just now';
  const date = new Date(value);
  return Number.isNaN(date.valueOf()) ? 'Just now' : date.toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' });
}

function money(value) {
  if (typeof value !== 'number') return value || 'Price unavailable';
  return new Intl.NumberFormat('id-ID', { style: 'currency', currency: 'IDR', maximumFractionDigits: 0 }).format(value);
}

function HackathonDemoPage() {
  const [draft, setDraft] = useState(demoBuyerRequest);
  const [publishedPost, setPublishedPost] = useState(null);
  const [postState, setPostState] = useState('idle');
  const [postError, setPostError] = useState('');
  const [discovery, setDiscovery] = useState(null);
  const [discoveryState, setDiscoveryState] = useState('idle');
  const [discoveryError, setDiscoveryError] = useState('');
  const [messages, setMessages] = useState([]);
  const [lead, setLead] = useState(null);
  const [syncError, setSyncError] = useState('');
  const [replyDraft, setReplyDraft] = useState('');
  const [replyState, setReplyState] = useState('idle');
  const [replyError, setReplyError] = useState('');
  const [savedBuyerReply, setSavedBuyerReply] = useState(null);
  const postPayload = useRef(null);
  const replyPayload = useRef(null);
  const discoveryStarted = useRef(false);

  const match = discovery?.matches?.[0] ?? null;
  const latestAgentMessage = useMemo(
    () => [...messages].reverse().find((message) => message.direction === 'agent'),
    [messages],
  );
  const checkoutMessage = useMemo(
    () => messages.find((message) => message.direction === 'agent' && /\/checkout\/[A-Za-z0-9_-]+/.test(message.text)),
    [messages],
  );
  const agentMessages = useMemo(() => messages.filter((message) => message.direction === 'agent'), [messages]);

  useEffect(() => {
    if (!match?.threadId || !match?.leadId) return undefined;
    let stopped = false;
    let timer;

    async function refreshEvidence() {
      const [threadResult, leadResult] = await Promise.allSettled([
        readHackathonThread(match.threadId),
        readHackathonLead(match.leadId),
      ]);
      if (stopped) return;

      if (threadResult.status === 'fulfilled') {
        setMessages(threadResult.value.messages ?? []);
        setSyncError('');
      } else {
        setSyncError(threadResult.reason?.message || 'Could not refresh the buyer inbox.');
      }

      if (leadResult.status === 'fulfilled') setLead(leadResult.value.data ?? null);
      timer = window.setTimeout(refreshEvidence, 1500);
    }

    refreshEvidence();
    return () => {
      stopped = true;
      window.clearTimeout(timer);
    };
  }, [match?.leadId, match?.threadId]);

  async function publishPost(event) {
    event.preventDefault();
    if (postState === 'saving' || publishedPost) return;
    const text = draft.trim();
    if (!text) return;

    postPayload.current ??= makeHackathonPost(text);
    setPostState('saving');
    setPostError('');
    try {
      const saved = await saveHackathonPost(postPayload.current);
      setPublishedPost(saved);
      setPostState('saved');
    } catch (error) {
      setPostState('failed');
      setPostError(error.message || 'The buyer post could not be saved.');
    }
  }

  async function runDiscovery() {
    if (!publishedPost || discoveryStarted.current) return;
    discoveryStarted.current = true;
    setDiscoveryState('searching');
    setDiscoveryError('');
    try {
      const result = await discoverHackathonBuyers();
      setDiscovery(result);
      setDiscoveryState(result.matches?.length ? 'matched' : 'empty');
    } catch (error) {
      setDiscoveryState('failed');
      setDiscoveryError(error.message || 'Discovery could not be completed.');
    }
  }

  async function submitBuyerReply(event) {
    event.preventDefault();
    const text = replyDraft.trim();
    if (!text || !match?.threadId || !latestAgentMessage || replyState === 'sending' || checkoutMessage) return;

    replyPayload.current ??= {
      externalReplyId: `judge-reply-${globalThis.crypto?.randomUUID?.() ?? `${Date.now()}-${Math.random().toString(36).slice(2)}`}`,
      replyToMessageId: latestAgentMessage.id,
      text,
    };
    setReplyState('sending');
    setReplyError('');
    try {
      const response = await sendHackathonReply(match.threadId, replyPayload.current);
      setSavedBuyerReply(response.message);
      setReplyDraft('');
      replyPayload.current = null;
      setReplyState('saved');
    } catch (error) {
      setReplyState('failed');
      setReplyError(error.message || 'The buyer reply could not be saved.');
    }
  }

  function useDemoReply() {
    setReplyDraft('I accept. Please send the checkout link.');
    replyPayload.current = null;
    setReplyState('idle');
    setReplyError('');
  }

  function updateReplyDraft(value) {
    if (replyPayload.current?.text !== value.trim()) replyPayload.current = null;
    setReplyDraft(value);
    if (replyState === 'failed') {
      setReplyState('idle');
      setReplyError('');
    }
  }

  const activity = [];
  if (publishedPost) activity.push({ id: 'post', title: 'Buyer post saved', detail: `Post ${publishedPost.external_post_id}`, time: clockTime(publishedPost.created_at), state: 'complete' });
  if (discoveryState === 'searching') activity.push({ id: 'discovery-loading', title: 'Searching seller inventory', detail: 'One discovery request is in progress', state: 'pending' });
  if (discovery) activity.push({ id: 'discovery', title: discovery.matches?.length ? 'Buyer matched to inventory' : 'Discovery completed', detail: discovery.runId ? `Run ${discovery.runId}` : `${discovery.requestsScanned ?? 0} posts · ${discovery.listingsScanned ?? 0} listings`, time: 'Confirmed by API', state: discovery.matches?.length ? 'complete' : 'muted' });
  if (match) activity.push({ id: 'lead', title: 'Lead persisted', detail: `Lead ${match.leadId}`, time: 'Confirmed by discovery', state: 'complete' });
  if (agentMessages[0]) activity.push({ id: 'outreach', title: 'Seller outreach delivered', detail: 'Message is visible in the saved buyer thread', time: clockTime(agentMessages[0].created_at), state: 'complete' });
  if (savedBuyerReply) activity.push({ id: 'buyer-reply', title: 'Buyer reply saved', detail: 'Reply is linked to the seller message', time: clockTime(savedBuyerReply.created_at), state: 'complete' });
  if (lead?.checkout?.status === 'pending') activity.push({ id: 'checkout', title: 'Mock checkout reserved', detail: `Lead state: ${lead.state}`, time: 'Confirmed by lead API', state: 'complete' });
  if (checkoutMessage) activity.push({ id: 'link', title: 'Link sent', detail: 'Checkout link is present in the saved seller message', time: clockTime(checkoutMessage.created_at), state: 'complete' });
  if (replyState === 'sending') activity.push({ id: 'reply-pending', title: 'Saving buyer reply', detail: 'Waiting for the marketplace API', state: 'pending' });
  if (discoveryState === 'failed') activity.push({ id: 'discovery-error', title: 'Discovery failed', detail: discoveryError, state: 'error' });

  const agentStatus = checkoutMessage
    ? 'Link sent'
    : agentMessages.length > 0
      ? 'Seller agent is active'
      : match
        ? 'Waiting for seller outreach'
        : discoveryState === 'searching'
          ? 'Discovery is running'
          : 'Waiting for a matched lead';

  return (
    <main className="judge-demo">
      <header className="judge-topbar">
        <a className="judge-brand" href="/" aria-label="Return to seller dashboard"><span className="judge-brand-mark">L</span>loopfind</a>
        <div className="judge-demo-title"><span className="judge-live-dot" /> HACKATHON DEMO <span>·</span> fixed-price journey</div>
        <div className="judge-top-actions"><span>Opt-in live mode</span><a href="/buyer">Buyer demo <b>↗</b></a></div>
      </header>

      <div className="judge-grid">
        <section className="judge-pane social-pane" aria-label="Social buyer post">
          <div className="judge-pane-heading"><span className="judge-pane-icon blue">01</span><div><strong>Social feed</strong><small>Buyer request</small></div><span className="judge-source-badge">MOCK SOCIAL</span></div>
          <div className="judge-pane-body">
            <form className="judge-post-composer" onSubmit={publishPost}>
              <div className="judge-composer-profile"><span className="judge-social-avatar">Y</span><div><strong>You</strong><small>Post to Laptop Deals &amp; Advice · Public</small></div></div>
              <textarea aria-label="Buyer social post" value={draft} onChange={(event) => setDraft(event.target.value)} disabled={postState === 'saving' || !!publishedPost} />
              <div className="judge-composer-footer"><span>{postState === 'saved' ? 'Saved with a backend post ID' : 'Public buyer intent post'}</span><button type="submit" disabled={!draft.trim() || postState === 'saving' || !!publishedPost}>{postState === 'saving' ? 'Saving…' : postState === 'failed' ? 'Retry save' : publishedPost ? 'Posted' : 'Post request'}</button></div>
              {postError && <p className="judge-inline-error" role="alert">{postError}</p>}
            </form>

            {publishedPost ? <article className="judge-social-post">
              <div className="judge-social-post-header"><span className="judge-group-avatar">💻</span><div><strong>Laptop Deals &amp; Advice</strong><small>You · Public post · {clockTime(publishedPost.created_at)}</small></div><span className="judge-post-saved">SAVED</span></div>
              <p>{publishedPost.text}</p>
              <div className="judge-social-post-footer"><span>👍 Like</span><span>◯ Comment</span><span>↗ Share</span></div>
            </article> : <div className="judge-empty-card"><span>↗</span><strong>Your request appears here</strong><small>Post once to save the buyer request and open its thread.</small></div>}
          </div>
        </section>

        <section className="judge-pane inbox-pane" aria-label="Buyer inbox">
          <div className="judge-pane-heading"><span className="judge-pane-icon violet">02</span><div><strong>Buyer inbox</strong><small>{match ? match.buyer.name : 'Waiting for a matched thread'}</small></div><span className={`judge-state-pill ${checkoutMessage ? 'success' : ''}`}>{checkoutMessage ? 'LINK SENT' : match ? 'LIVE THREAD' : 'NO THREAD'}</span></div>
          <div className="judge-inbox-context">
            <div><span className="judge-social-avatar small">Y</span><span><strong>{match?.buyer.name ?? 'Buyer'}</strong><small>{publishedPost?.text ?? 'The saved buyer post will appear in this thread.'}</small></span></div>
          </div>
          <div className="judge-chat-scroll" aria-live="polite">
            {messages.length === 0 && <div className="judge-chat-empty">{match ? 'Waiting for the seller agent’s first delivered message…' : 'The buyer and seller messages will appear here after matching.'}</div>}
            {messages.map((message) => <article className={`judge-message ${message.direction === 'agent' ? 'agent' : 'buyer'}`} key={message.id}>
              <span>{message.direction === 'agent' ? 'Seller agent' : match?.buyer.name ?? 'Buyer'} <time>{clockTime(message.created_at)}</time></span>
              <p>{message.text}</p>
            </article>)}
            {syncError && <p className="judge-inline-error">Inbox refresh: {syncError}</p>}
          </div>
          <form className="judge-reply-composer" onSubmit={submitBuyerReply}>
            <div className="judge-reply-heading"><strong>Reply as the buyer</strong><button type="button" onClick={useDemoReply} disabled={!latestAgentMessage || !!checkoutMessage}>Use demo reply</button></div>
            <textarea aria-label="Buyer reply message" placeholder={latestAgentMessage ? 'Ask a question or accept the offer and ask for checkout…' : 'A seller message must arrive before the buyer can reply.'} value={replyDraft} onChange={(event) => updateReplyDraft(event.target.value)} disabled={!latestAgentMessage || !!checkoutMessage || replyState === 'sending'} />
            <div className="judge-reply-footer"><span>{replyState === 'saved' ? 'Saved · waiting for the agent to respond' : replyState === 'sending' ? 'Saving reply…' : checkoutMessage ? 'The delivered checkout link ends this demo flow.' : 'Reply is attached to the latest seller message.'}</span><button type="submit" disabled={!replyDraft.trim() || !latestAgentMessage || !!checkoutMessage || replyState === 'sending'}>{replyState === 'sending' ? 'Sending…' : 'Send reply'}</button></div>
            {replyError && <div className="judge-inline-error" role="alert"><span>{replyError}</span><button type="button" onClick={submitBuyerReply}>Retry same reply</button></div>}
          </form>
        </section>

        <section className="judge-pane seller-pane" aria-label="Seller dashboard">
          <div className="judge-pane-heading"><span className="judge-pane-icon orange">03</span><div><strong>Seller dashboard</strong><small>Northside Reseller · {hackathonSellerId}</small></div><span className={`judge-state-pill ${match ? 'success' : ''}`}>{match ? 'MATCH' : 'DISCOVER'}</span></div>
          <div className="judge-seller-body">
            <div className="judge-seller-intro"><span className="judge-eyebrow">SELLER DISCOVERY</span><h1>Find the right buyer</h1><p>Run discovery against the saved request and the seller’s available inventory.</p></div>
            <button className="judge-discover-button" type="button" onClick={runDiscovery} disabled={!publishedPost || discoveryState !== 'idle'}>
              {discoveryState === 'searching' ? <><i className="judge-spinner" /> Searching inventory…</> : discoveryState === 'matched' ? 'Discovery complete' : discoveryState === 'empty' ? 'No match found' : discoveryState === 'failed' ? 'Discovery failed' : '⌕  Discover buyers'}
            </button>
            {!publishedPost && <p className="judge-hint">Save the social post before starting discovery.</p>}
            {discoveryError && <p className="judge-inline-error" role="alert">{discoveryError}</p>}
            {discovery?.warnings?.length > 0 && <p className="judge-warning">{discovery.warnings[0].message ?? 'Discovery completed with a warning.'}</p>}

            {match ? <article className="judge-match-card">
              <div className="judge-match-card-top"><span className="judge-match-label">BEST MATCH</span><strong>{match.matchScore}%</strong></div>
              <div className="judge-product-visual"><span>💻</span><small>USED LAPTOP</small></div>
              <div className="judge-match-product"><span>SELLER INVENTORY</span><h2>{match.product.name}</h2><p>{match.product.condition || 'Used'} · {match.product.availability}</p><div className="judge-product-price"><small>LIST PRICE</small><strong>{money(match.product.priceIDR)}</strong></div></div>
              <div className="judge-match-reason"><span>WHY IT MATCHED</span><p>{match.matchReasons?.[0] ?? 'Matched to the buyer request and seller inventory.'}</p></div>
              <div className="judge-lead-status"><span>Lead saved</span><strong>{lead?.state ?? match.leadState ?? 'active'}</strong></div>
              <div className={`judge-agent-status ${checkoutMessage ? 'delivered' : ''}`}><i />{agentStatus}</div>
            </article> : discoveryState === 'empty' ? <div className="judge-empty-card compact"><strong>No suitable item was found</strong><small>The API returned no eligible seller match for this post.</small></div> : <div className="judge-inventory-placeholder"><span>INVENTORY</span><strong>ThinkPad T480 · 16 GB RAM</strong><small>Rp 5,800,000 · 2 available</small></div>}
            {lead?.offer && <div className="judge-offer-line"><span>Current agent offer</span><strong>{money(lead.offer.price_idr)}</strong></div>}
            {lead?.checkout?.status === 'pending' && <div className="judge-reservation-note">Reserved after buyer acceptance. The checkout itself stays outside this shared screen.</div>}
          </div>
        </section>

        <aside className="judge-pane activity-pane" aria-label="Activity log">
          <div className="judge-pane-heading"><span className="judge-pane-icon dark">04</span><div><strong>Activity log</strong><small>Confirmed events</small></div></div>
          <div className="judge-activity-list" aria-live="polite">
            {activity.length === 0 && <div className="judge-activity-empty">Actions appear here after the APIs confirm them.</div>}
            {activity.map((event) => <article className={`judge-activity-event ${event.state}`} key={event.id}>
              <i />
              <div><strong>{event.title}</strong><p>{event.detail}</p>{event.time && <time>{event.time}</time>}</div>
            </article>)}
            {replyState === 'failed' && <article className="judge-activity-event error"><i /><div><strong>Reply not confirmed</strong><p>{replyError}</p></div></article>}
          </div>
          <div className="judge-activity-note"><strong>Delivery rule</strong><p>“Link sent” appears only after the checkout URL is present in the saved seller message.</p></div>
        </aside>
      </div>
    </main>
  );
}

export default HackathonDemoPage;
