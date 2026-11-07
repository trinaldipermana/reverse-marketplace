import { buildBuyerPost } from './buyerPosts.js';

export const hackathonSellerId = 'seller_demo';
export const demoBuyerRequest = 'I need a used laptop for coding with at least 16 GB RAM, under Rp6 million, in Jakarta this week.';

const demoHeaders = { 'X-Demo-Mode': 'hackathon' };

async function request(path, options = {}, timeoutMs = 12000) {
  const controller = new AbortController();
  const timeout = window.setTimeout(() => controller.abort(), timeoutMs);
  try {
    const response = await fetch(path, {
      ...options,
      headers: { ...demoHeaders, ...(options.headers ?? {}) },
      signal: controller.signal,
    });
    const body = await response.json().catch(() => null);
    if (!response.ok) {
      const message = body?.message ?? body?.error?.message ?? body?.error?.code ?? `Request failed (${response.status})`;
      throw new Error(message);
    }
    return body;
  } finally {
    window.clearTimeout(timeout);
  }
}

export function makeHackathonPost(text) {
  return {
    ...buildBuyerPost(text),
    location: { city: 'Jakarta' },
  };
}

export function saveHackathonPost(post) {
  return request('/api/hackathon/mock/posts', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'Idempotency-Key': `hackathon-${post.external_post_id}`,
    },
    body: JSON.stringify(post),
  });
}

export function discoverHackathonBuyers() {
  return request('/api/hackathon/matches/search', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ sellerId: hackathonSellerId }),
  }, 100000);
}

export function readHackathonThread(threadId) {
  return request(`/api/hackathon/mock/threads/${encodeURIComponent(threadId)}`);
}

export function sendHackathonReply(threadId, reply) {
  return request(`/api/hackathon/mock/threads/${encodeURIComponent(threadId)}/replies`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      external_reply_id: reply.externalReplyId,
      reply_to_message_id: reply.replyToMessageId,
      text: reply.text,
    }),
  });
}

export function readHackathonLead(leadId) {
  return request(`/api/hackathon/leads/${encodeURIComponent(leadId)}`, {
    headers: { 'X-Seller-ID': hackathonSellerId },
  });
}
