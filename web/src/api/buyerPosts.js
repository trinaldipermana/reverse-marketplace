export const buyerDemoThreadId = 'thread_buyer_demo';

async function request(path, options = {}) {
  const response = await fetch(path, options);
  const body = await response.json().catch(() => null);
  if (!response.ok) {
    const message = body?.message ?? body?.error?.message ?? `Request failed (${response.status})`;
    throw new Error(message);
  }
  return body;
}

function newExternalId(prefix) {
  const random = globalThis.crypto?.randomUUID?.() ?? `${Date.now()}-${Math.random().toString(36).slice(2)}`;
  return `${prefix}-${random}`;
}

export function createBuyerPost(content) {
  const externalPostId = newExternalId('buyer-post');
  return request('/api/mock/posts', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'Idempotency-Key': `web-${externalPostId}`,
    },
    body: JSON.stringify({
      platform: 'mock',
      external_post_id: externalPostId,
      buyer_id: 'buyer_alex',
      thread_id: `thread-${externalPostId}`,
      text: content,
      created_at: new Date().toISOString(),
    }),
  });
}

export function getBuyerThread() {
  return request(`/api/mock/threads/${encodeURIComponent(buyerDemoThreadId)}`);
}

export function submitBuyerReply(text, externalReplyId) {
  return request(`/api/mock/threads/${encodeURIComponent(buyerDemoThreadId)}/replies`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ external_reply_id: externalReplyId, text }),
  });
}
