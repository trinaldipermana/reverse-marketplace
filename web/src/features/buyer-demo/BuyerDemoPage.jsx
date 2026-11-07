import { useEffect, useState } from 'react';
import { createBuyerPost, getBuyerThread, submitBuyerReply } from '../../api/buyerPosts.js';
import './buyer-demo.css';

const productUrl = 'https://shopee.co.id/Alesis-Nitro-Max-Electronic-Drum-Kit-Mesh-Head-Bluetooth-%E2%80%93-Set-Drum-Elektrik-Lengkap-i.1408254222.50702962973';
const initialPosts = [{
  id: 'matched-request',
  content: 'Searching for a beginner-friendly electronic drum set. Open to used options nearby.',
  time: '1 hr',
  matched: true,
}];

export default function BuyerDemoPage() {
  const [reply, setReply] = useState('');
  const [buyerMessages, setBuyerMessages] = useState([]);
  const [chatState, setChatState] = useState('loading');
  const [postDraft, setPostDraft] = useState('');
  const [posts, setPosts] = useState(initialPosts);
  const [postState, setPostState] = useState('idle');

  useEffect(() => {
    let active = true;
    getBuyerThread().then((result) => {
      if (!active) return;
      setBuyerMessages((result.messages ?? []).filter((message) => message.direction === 'buyer').map((message) => ({
        ...message,
        clientId: message.external_reply_id || message.id,
        delivery: 'sent',
      })));
      setChatState('ready');
    }).catch(() => {
      if (active) setChatState('unavailable');
    });
    return () => { active = false; };
  }, []);

  async function sendReply(event) {
    event.preventDefault();
    const text = reply.trim();
    if (!text) return;
    const externalReplyId = `buyer-reply-${globalThis.crypto?.randomUUID?.() ?? `${Date.now()}-${Math.random().toString(36).slice(2)}`}`;
    setBuyerMessages((current) => [...current, {
      clientId: externalReplyId,
      external_reply_id: externalReplyId,
      text,
      created_at: new Date().toISOString(),
      delivery: 'sending',
    }]);
    setReply('');
    await deliverReply(text, externalReplyId);
  }

  async function deliverReply(text, externalReplyId) {
    try {
      const result = await submitBuyerReply(text, externalReplyId);
      setBuyerMessages((current) => current.map((message) => message.clientId === externalReplyId
        ? { ...result.message, clientId: externalReplyId, delivery: 'sent' }
        : message));
      setChatState('ready');
    } catch {
      setBuyerMessages((current) => current.map((message) => message.clientId === externalReplyId
        ? { ...message, delivery: 'failed' }
        : message));
      setChatState('unavailable');
    }
  }

  function retryReply(message) {
    setBuyerMessages((current) => current.map((item) => item.clientId === message.clientId
      ? { ...item, delivery: 'sending' }
      : item));
    deliverReply(message.text, message.external_reply_id);
  }

  async function publishPost(event) {
    event.preventDefault();
    const content = postDraft.trim();
    if (!content || postState === 'sending') return;

    const postId = `post-${Date.now()}`;
    setPosts((current) => [{ id: postId, content, time: 'just now', matched: false }, ...current]);
    setPostDraft('');
    setPostState('sending');
    try {
      const result = await createBuyerPost(content);
      setPosts((current) => current.map((post) => post.id === postId
        ? { ...post, id: result.external_post_id || post.id, apiStatus: 'sent' }
        : post));
      setPostState('sent');
    } catch {
      setPostState('failed');
    }
  }

  return (
    <main className="buyer-demo">
      <header className="buyer-topbar">
        <a className="buyer-brand social-brand" href="/" aria-label="Loopfind seller dashboard"><span className="buyer-brand-mark">f</span><span>social</span></a>
        <div className="buyer-top-note"><span className="buyer-status-dot" /> Social inbox preview</div>
        <a className="seller-view-link" href="/">Back to Loopfind <span>↗</span></a>
      </header>

      <div className="buyer-layout">
        <section className="buyer-intro">
          <div className="buyer-kicker">YOUR SOCIAL FEED</div>
          <h1>Your post caught a seller’s eye.</h1>
          <p>You asked the community for an electronic drum kit. Northside Music saw your public post and sent a product suggestion in a direct message.</p>
          <form className="social-composer" onSubmit={publishPost}>
            <div className="composer-user-avatar">A</div>
            <div className="composer-input-wrap"><label className="visually-hidden" htmlFor="social-post-input">Write a public post</label><textarea id="social-post-input" value={postDraft} onChange={(event) => setPostDraft(event.target.value)} placeholder="What's on your mind, Alex?" maxLength={500} /></div>
            <div className="composer-submit-row"><span>{postDraft.length}/500 characters</span><button type="submit" disabled={!postDraft.trim() || postState === 'sending'}>{postState === 'sending' ? 'Posting…' : 'Post'}</button></div>
            <p className={`post-api-status ${postState}`} aria-live="polite">{postState === 'sent' ? 'Posted to the mock marketplace API.' : postState === 'failed' ? 'Added to your feed, but the API request failed.' : postState === 'sending' ? 'Saving your post to the marketplace API…' : 'Posts are saved to the mock marketplace API.'}</p>
          </form>
          <div className="social-post-list" aria-live="polite">
            {posts.map((post) => <article className="social-post-card" key={post.id}>
              <div className="social-post-top"><span className="social-group-avatar">♪</span><div><strong>Electronic Drum Kits &amp; Gear</strong><span>Public group · Alex Rivera · {post.time} <b>· 🌐</b></span></div><button type="button" aria-label="Post options">•••</button></div>
              <div className="social-post-text">{post.content}</div>
              {post.matched && <><div className="social-post-attachment"><div className="social-drum-art"><span>♬</span><i>WANTED</i></div><div><strong>Looking for recommendations</strong><span>Electronic drum kit · Beginner · Open to used</span></div></div><div className="social-reaction-summary"><span>👍 ❤️</span> 12 <span>3 comments</span></div></>}
              <div className="social-post-actions"><span>👍 Like</span><span>◯ Comment</span><span>↗ Share</span></div>
              {post.matched && <div className="social-post-match"><span>✦</span> A seller found a product match for this post</div>}
            </article>)}
          </div>
          <div className="buyer-privacy"><span>✓</span><div><strong>You stay in control</strong><p>This demo shows the seller starting the conversation. You can review the product link and choose whether to reply.</p></div></div>
        </section>

        <section className="buyer-chat-card" aria-label="Social direct message from Northside Music">
          <div className="buyer-chat-header">
            <div className="seller-avatar">N<span>✓</span></div>
            <div className="seller-details"><strong>Northside Music</strong><span><i /> Seller · Electronic drums</span><small>DIRECT MESSAGE</small></div>
            <div className="social-chat-mark">f</div>
            <button className="buyer-more" type="button" aria-label="More options">•••</button>
          </div>
          <div className="buyer-chat-body">
            <div className="buyer-chat-date">TODAY, 10:14 AM</div>
            <div className="seller-message-label">Northside Music</div>
            <div className="seller-message">Hi Alex! I saw your post looking for a beginner-friendly electronic drum set. I have an Alesis Nitro Max that could be a good fit. It’s open box and available now.</div>

            <a className="buyer-product-card" href={productUrl} target="_blank" rel="noreferrer">
              <div className="buyer-product-art"><span>♬</span><small>ALesis<br />NITRO MAX</small><i>Electronic drum kit</i></div>
              <div className="buyer-product-info">
                <div className="buyer-product-brand"><span className="shopee-badge">S</span> SHOPEE LISTING <span className="product-fit">94% MATCH</span></div>
                <strong>Alesis Nitro Max Electronic Drum Kit</strong>
                <span className="buyer-product-meta">Open box <b>·</b> Available now</span>
                <span className="buyer-product-cta">View product on Shopee <b>↗</b></span>
              </div>
            </a>
            <div className="buyer-link-note"><span>↗</span> Product link shared in your social inbox</div>
            <div className={`chat-api-status ${chatState}`} aria-live="polite">{chatState === 'loading' ? 'Loading saved replies…' : chatState === 'unavailable' ? 'Mock chat API is unavailable. Your draft stays here; retry when it is online.' : 'Replies are saved to the mock conversation API.'}</div>
            {buyerMessages.map((message) => <div className="buyer-sent-message" key={message.clientId}>
              <span>Alex · {message.delivery === 'sending' ? 'sending…' : message.delivery === 'failed' ? 'not sent' : 'sent'}</span>
              <p>{message.text}</p>
              {message.delivery === 'failed' && <button className="buyer-message-retry" type="button" onClick={() => retryReply(message)}>Retry</button>}
            </div>)}
          </div>
          <form className="buyer-reply" onSubmit={sendReply}>
            <label htmlFor="buyer-reply-input">Reply to Northside Music</label>
            <div className="buyer-reply-field"><input id="buyer-reply-input" value={reply} onChange={(event) => setReply(event.target.value)} placeholder="Ask a question or start a conversation…" /><button type="submit" disabled={!reply.trim()} aria-label="Send reply">↑</button></div>
            <div className="buyer-reply-foot"><span>Only reply if you’re interested</span><span>🔒 Private conversation</span></div>
          </form>
        </section>
      </div>
      <footer className="buyer-footer"><span>loopfind <b>·</b> connecting buyer requests with relevant products</span><span>Mock marketplace API · no external social message is sent</span></footer>
    </main>
  );
}
