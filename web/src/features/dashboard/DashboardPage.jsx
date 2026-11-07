import { useMemo, useState } from 'react';
import { createConversation, searchBuyerMatches } from '../../api/matches.js';
import Avatar from '../../components/Avatar.jsx';
import Icon from '../../components/Icon.jsx';

const initialThreads = [
  {
    id: 'sam',
    name: 'Sam J.',
    initials: 'SJ',
    product: 'Alesis Nitro Mesh Kit',
    category: 'Electric drums',
    source: 'Reddit · r/edrums',
    time: '2m',
    stage: 'Offer shared',
    unread: true,
    buyerPost: 'Looking for an electric drum kit for an apartment. Quiet practice and mesh pads are important.',
    listing: 'Alesis Nitro Mesh Kit · used, good condition',
    price: '$289',
    offer: '$270 pickup',
    referral: '$14.45',
    messages: [
      { from: 'seller', text: 'Hi Sam, I have a used Alesis Nitro Mesh kit that fits your quiet practice needs. I can send photos and details.', time: '10:14' },
      { from: 'buyer', text: 'Thanks! Does it include the kick pedal and the original module?', time: '10:19' },
      { from: 'seller', text: 'Yes, both are included. I’ve added the listing and a tracked purchase link in the offer.', time: '10:22' },
      { from: 'buyer', text: 'Could you do $270 if I pick it up?', time: '10:27' },
    ],
    events: [
      ['Matched to buyer request', 'Today · 10:02', 'done'],
      ['Seller started conversation', 'Today · 10:14', 'done'],
      ['Offer shared with tracked link', 'Today · 10:22', 'done'],
      ['Purchase confirmation', 'Waiting for buyer', 'pending'],
    ],
  },
  {
    id: 'maya',
    name: 'Maya L.',
    initials: 'ML',
    product: 'Roland TD-02K',
    category: 'Compact drum kit',
    source: 'Facebook public post',
    time: '18m',
    stage: 'DM started',
    unread: true,
    buyerPost: 'Need a compact beginner kit that will fit in a small bedroom. Hoping to stay under $450.',
    listing: 'Roland TD-02K · new, warranty included',
    price: '$399',
    offer: 'No offer yet',
    referral: '$19.95',
    messages: [
      { from: 'seller', text: 'Hi Maya, I saw your request for a compact electronic kit. I have a Roland TD-02K that may suit your space.', time: '9:40' },
      { from: 'buyer', text: 'Is the kit still available?', time: '9:47' },
    ],
    events: [
      ['Matched to buyer request', 'Today · 9:34', 'done'],
      ['Seller started conversation', 'Today · 9:40', 'done'],
      ['Offer shared', 'Not yet', 'pending'],
      ['Purchase confirmation', 'Not yet', 'pending'],
    ],
  },
  {
    id: 'alex',
    name: 'Alex R.',
    initials: 'AR',
    product: 'Alesis Nitro Max',
    category: 'Used drum kit',
    source: 'Marketplace public post',
    time: '1h',
    stage: 'New match',
    unread: false,
    buyerPost: 'Searching for a beginner-friendly electronic drum set. Open to used options nearby.',
    listing: 'Alesis Nitro Max · open box',
    price: '$329',
    offer: 'No offer yet',
    referral: '$16.45',
    messages: [],
    events: [
      ['Matched to buyer request', 'Today · 9:02', 'done'],
      ['Seller starts conversation', 'Next step', 'pending'],
      ['Offer shared', 'Not yet', 'pending'],
      ['Purchase confirmation', 'Not yet', 'pending'],
    ],
  },
  {
    id: 'jordan',
    name: 'Jordan T.',
    initials: 'JT',
    product: 'Yamaha DTX402K',
    category: 'Electric drum kit',
    source: 'Instagram public post',
    time: 'Yesterday',
    stage: 'Purchased',
    unread: false,
    buyerPost: 'Any recommendations for a reliable electronic drum kit for home practice?',
    listing: 'Yamaha DTX402K · new',
    price: '$419',
    offer: '$419',
    referral: '$20.95',
    messages: [
      { from: 'seller', text: 'Hi Jordan, your home practice request matched a Yamaha kit with quiet pads. Here is the listing and details.', time: 'Yesterday' },
      { from: 'buyer', text: 'The link worked, order placed!', time: 'Yesterday' },
    ],
    events: [
      ['Matched to buyer request', 'Yesterday · 13:21', 'done'],
      ['Seller started conversation', 'Yesterday · 13:30', 'done'],
      ['Offer shared with tracked link', 'Yesterday · 13:42', 'done'],
      ['Purchase confirmed', 'Yesterday · 16:08', 'done'],
    ],
  },
];

const filters = ['All', 'Needs reply', 'New matches'];

function DashboardPage() {
  const [threads, setThreads] = useState(initialThreads);
  const [activeId, setActiveId] = useState('sam');
  const [filter, setFilter] = useState('All');
  const [query, setQuery] = useState('');
  const [draft, setDraft] = useState('');
  const [matchState, setMatchState] = useState('idle');
  const [matchResult, setMatchResult] = useState(null);
  const [matchSummary, setMatchSummary] = useState(null);
  const [matchError, setMatchError] = useState('');

  const active = threads.find((thread) => thread.id === activeId) ?? threads[0];
  const visibleThreads = useMemo(() => threads.filter((thread) => {
    const matchesQuery = `${thread.name} ${thread.product} ${thread.category}`.toLowerCase().includes(query.toLowerCase());
    const matchesFilter = filter === 'All'
      || (filter === 'Needs reply' && thread.unread)
      || (filter === 'New matches' && thread.stage === 'New match')
      || (filter === 'Running sales' && ['DM started', 'Offer shared'].includes(thread.stage));
    return matchesQuery && matchesFilter;
  }), [threads, query, filter]);

  function selectThread(thread) {
    setActiveId(thread.id);
    setDraft('');
    setThreads((current) => current.map((item) => item.id === thread.id ? { ...item, unread: false } : item));
  }

  async function runMatchSearch() {
    setMatchState('loading');
    setMatchError('');
    try {
      const response = await searchBuyerMatches('northside-music');
      setMatchSummary({ requests: response.requestsScanned, listings: response.listingsScanned });
      setMatchResult(response.matches?.[0] ?? null);
      setMatchState(response.matches?.length ? 'found' : 'empty');
    } catch (error) {
      setMatchError(error.message || 'Could not search for buyer matches. Try again.');
      setMatchState('error');
    }
  }

  async function startConversation(target = active, match = matchResult) {
    if (!target || !['New match', 'DM started'].includes(target.stage)) return;
    if (target.stage === 'DM started') {
      setActiveId(target.id);
      document.getElementById('conversation-area')?.scrollIntoView({ behavior: 'smooth', block: 'start' });
      return;
    }

    try {
      await createConversation(match?.matchId ?? target.id, match?.buyer.id, match?.product.id);
      const buyer = match?.buyer;
      const product = match?.product;
      setThreads((current) => current.map((thread) => thread.id === target.id ? {
        ...thread,
        name: buyer?.name ?? thread.name,
        initials: buyer?.initials ?? thread.initials,
        product: product?.name ?? thread.product,
        category: product?.category ?? thread.category,
        source: buyer?.source ?? thread.source,
        buyerPost: buyer?.post ?? thread.buyerPost,
        listing: product ? `${product.name} · ${product.condition}` : thread.listing,
        price: product?.price ?? thread.price,
        productUrl: product?.url ?? thread.productUrl,
        stage: 'DM started',
        unread: false,
        messages: [],
        events: thread.events.map(([label, time, status]) => label === 'Seller starts conversation'
          ? ['Seller started conversation', 'Now', 'done']
          : [label, time, status]),
      } : thread));
      setActiveId(target.id);
      setDraft('');
      document.getElementById('conversation-area')?.scrollIntoView({ behavior: 'smooth', block: 'start' });
    } catch (error) {
      setMatchError(error.message || 'Could not start the conversation. Try again.');
    }
  }

  function sendMessage(event) {
    event.preventDefault();
    const text = draft.trim();
    if (!text || !active || active.stage === 'New match') return;
    setThreads((current) => current.map((thread) => thread.id === active.id
      ? { ...thread, messages: [...thread.messages, { from: 'seller', text, time: 'Now' }] }
      : thread));
    setDraft('');
  }

  return (
    <div className="app-shell">
      <aside className="sidebar">
        <div className="brand-lockup"><div className="brand-mark">L</div><span>loopfind</span></div>
        <button className="workspace-switch" type="button">
          <Avatar initials="NS" small />
          <span><strong>Northside Music</strong><small>Seller workspace</small></span>
          <Icon>⌄</Icon>
        </button>
        <nav className="side-nav" aria-label="Main navigation">
          <a className="nav-item" href="#find-buyer"><Icon>⌕</Icon>Find buyer</a>
          <a className="nav-item selected" href="#conversation-area"><Icon>▤</Icon>Conversations<span className="nav-count">{threads.length}</span></a>
        </nav>
        <div className="sidebar-bottom">
          <button className="profile-button" type="button"><Avatar initials="NS" /><span><strong>Northside Music</strong><small>Seller account</small></span><Icon>•••</Icon></button>
        </div>
      </aside>

      <main className="main-area">
        <header className="topbar">
          <div className="breadcrumb"><strong className="top-brand">loopfind</strong><span className="top-divider" /><span>Seller workspace</span><Icon>›</Icon><strong>Find buyers &amp; conversations</strong></div>
          <div className="top-actions"><span className="connection"><i /> Sources connected</span><a className="buyer-demo-link" href="/buyer">Buyer demo <span>↗</span></a><button aria-label="Notifications" className="icon-button" type="button">♧<i className="notification-dot" /></button><Avatar initials="NS" small /></div>
        </header>

        <div className="page-content">
          <section className="match-search-card" id="find-buyer" aria-live="polite">
            <div className="match-search-head">
              <div className="match-search-icon">⌕</div>
              <div className="match-search-copy"><span className="eyebrow">BUYER MATCHING</span><h2>Find a buyer for your products</h2><p>Search public buyer requests and match them against your active catalog.</p></div>
              <button className="button button-primary search-button" type="button" onClick={runMatchSearch} disabled={matchState === 'loading'}><span>⌕</span>{matchState === 'loading' ? 'Searching…' : matchState === 'found' ? 'Search again' : 'Search now'}</button>
            </div>
            {matchState === 'idle' && <div className="search-hint"><span>↳</span> Search calls the matching API and returns the single best product–buyer match for review.</div>}
            {matchState === 'loading' && <div className="search-state"><span className="spinner" /> Scanning recent buyer intent against your product catalog…</div>}
            {matchState === 'empty' && <div className="search-state">No suitable buyer matches found. Try again later.</div>}
            {matchError && <div className="search-error" role="alert">{matchError}</div>}
            {matchResult && matchState === 'found' && <article className="match-preview-card">
              <div className="preview-banner"><span>BEST MATCH</span><span>{matchSummary?.requests ?? 0} buyer posts scanned · {matchSummary?.listings ?? 0} listings scanned</span><strong>{matchResult.matchScore}% fit</strong></div>
              <div className="preview-columns">
                <div className="preview-buyer"><div className="preview-label">POTENTIAL BUYER</div><div className="preview-person"><Avatar initials={matchResult.buyer.initials} /><div><strong>{matchResult.buyer.name}</strong><span>{matchResult.buyer.source} · {matchResult.buyer.postedAt}</span></div></div><blockquote>“{matchResult.buyer.post}”</blockquote><div className="match-reasons">{matchResult.matchReasons.map((reason) => <span key={reason}>✓ {reason}</span>)}</div></div>
                <div className="preview-product"><div className="preview-label">YOUR MATCHING PRODUCT</div><div className="preview-product-row"><div className="product-thumbnail">♬</div><div><strong>{matchResult.product.name}</strong><span>{matchResult.product.condition} · {matchResult.product.availability}</span><small>{matchResult.product.category}</small></div><b>{matchResult.product.price}</b></div><a className="shopee-link" href={matchResult.product.url} target="_blank" rel="noreferrer"><span>S</span> View on Shopee ↗</a><p>This is the single highest-ranked listing for this buyer request.</p><button className="button button-primary start-dm-button" type="button" onClick={() => { const target = threads.find((thread) => thread.id === matchResult.threadId); if (target) startConversation(target, matchResult); }}>{threads.find((thread) => thread.id === matchResult.threadId)?.stage === 'DM started' ? 'Open conversation' : 'Start DM with buyer'} <span>→</span></button></div>
              </div>
            </article>}
          </section>

          <section className="workspace-grid" id="conversation-area" aria-label="Conversations">
            <aside className="inbox-panel">
              <div className="inbox-heading"><div><h2>Conversations</h2><span>{threads.length} total <i>·</i> {threads.filter((thread) => thread.unread).length} unread</span></div><button className="dots-button" aria-label="More inbox options" type="button">•••</button></div>
              <label className="search-box"><Icon>⌕</Icon><input aria-label="Search conversations" placeholder="Search buyers or products" value={query} onChange={(event) => setQuery(event.target.value)} /><kbd>⌘ K</kbd></label>
              <div className="filter-row" role="tablist" aria-label="Filter conversations">
                {filters.map((item) => <button key={item} type="button" role="tab" aria-selected={filter === item} className={filter === item ? 'filter active' : 'filter'} onClick={() => setFilter(item)}>{item}</button>)}
              </div>
              <div className="thread-list">
                {visibleThreads.length === 0 && <div className="empty-list">No conversations match this view.</div>}
                {visibleThreads.map((thread) => <button key={thread.id} type="button" className={`thread-row ${active.id === thread.id ? 'current' : ''}`} onClick={() => selectThread(thread)}>
                  <Avatar initials={thread.initials} />
                  <span className="thread-copy"><span className="thread-title"><strong>{thread.name}</strong><time>{thread.time}</time></span><span className="thread-snippet">{thread.messages.length ? thread.messages[thread.messages.length - 1].text : thread.buyerPost}</span><span className="thread-footer"><span className={`status-pill ${thread.stage === 'Purchased' || thread.stage === 'Offer shared' ? 'positive' : thread.stage === 'New match' ? 'new' : ''}`}>{thread.stage === 'New match' && <i />} {thread.stage}</span><span>{thread.category}</span></span></span>
                  {thread.unread && <i className="unread-dot" />}
                </button>)}
              </div>
              <button className="inbox-footer" type="button">Open all conversations <span>→</span></button>
            </aside>

            <section className="conversation-panel">
              <div className="conversation-header">
                <div className="buyer-heading"><Avatar initials={active.initials} /><div><strong>{active.name}</strong><span>Buyer request · {active.category}</span></div><span className="source-tag"><Icon>◉</Icon>{active.source}</span></div>
                <div className="conversation-actions"><button className="icon-button" aria-label="More conversation actions" type="button">•••</button><button className="button button-outline" type="button">View original post <span>↗</span></button></div>
              </div>

              {active.stage === 'New match' && active.messages.length === 0 && <div className="new-match-banner"><span className="spark">✦</span><span><strong>New buyer intent matched</strong><small>Review the public request and matched product preview, then start the DM.</small></span><span className="match-quality">Strong match · 94%</span></div>}

              <div className="chat-scroll">
                <div className="date-divider"><span />TODAY<span /></div>
                <article className="request-context"><div className="context-title"><span className="context-icon">⌕</span><div><strong>Original buyer request</strong><small>Detected in a public post · {active.source}</small></div><button type="button" className="context-link">Open post ↗</button></div><p>“{active.buyerPost}”</p><div className="request-tags"><span>Buying intent detected</span><span>{active.category}</span><span>Exact product match</span></div></article>
                <div className="match-card"><span className="match-product-icon">♬</span><div className="match-product-copy"><small>YOUR MATCHING LISTING</small><strong>{active.listing}</strong><span>Catalog match · available now</span></div><span className="match-score">94% fit</span></div>
                {active.stage === 'New match' && active.messages.length === 0 ? <div className="start-prompt"><div className="start-icon">↗</div><strong>Ready to contact this buyer?</strong><p>The buyer request and matching product are ready. Start the DM from here.</p><button className="button button-primary" type="button" onClick={() => startConversation(active, matchResult)}>Start a DM <span>→</span></button></div> : <div className="messages-list" aria-live="polite">
                  <div className="system-message"><span>✦</span> {active.messages.length ? 'Match found · you started this conversation' : 'DM started · write your first message'}</div>
                  {active.messages.map((message, index) => <div className={`message-line ${message.from === 'seller' ? 'outgoing' : 'incoming'}`} key={`${active.id}-${index}`}>
                    {message.from === 'buyer' && <Avatar initials={active.initials} small />}
                    <div className="message-content"><div className="message-author">{message.from === 'seller' ? 'You' : active.name} <time>{message.time}</time></div><div className="message-bubble">{message.text}</div></div>
                    {message.from === 'seller' && <Avatar initials="NS" small />}
                  </div>)}
                </div>}
              </div>
              <form className={`message-composer ${active.stage === 'New match' ? 'composer-disabled' : ''}`} onSubmit={sendMessage}>
                <textarea aria-label="Write a message" placeholder={active.stage === 'New match' ? 'Start a DM to contact this buyer' : 'Write a message…'} value={draft} onChange={(event) => setDraft(event.target.value)} disabled={active.stage === 'New match'} />
                <div className="composer-bottom"><div className="composer-hints"><span>↵ Enter to send</span><span>·</span><span>Messages stay linked to this match</span></div><div><button className="icon-button" type="button" aria-label="Attach file">＋</button><button className="button button-primary send-button" type="submit" disabled={active.stage === 'New match' || !draft.trim()}>Send <span>↑</span></button></div></div>
              </form>
            </section>

          </section>
        </div>
      </main>
    </div>
  );
}

export default DashboardPage;
