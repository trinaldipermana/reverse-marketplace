export const sellerMockMode = import.meta.env.VITE_SELLER_MOCK === 'true';
export const sellerId = import.meta.env.VITE_SELLER_ID || 'seller_demo';
async function request(path, options = {}) {
  const response = await fetch(path, options);
  const body = await response.json().catch(() => null);
  if (!response.ok) throw new Error(body?.error?.message || body?.message || `API request failed (${response.status})`);
  if (!body) throw new Error('The API returned an invalid response.');
  return body;
}
function post(path, payload) { return request(path, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(payload) }); }
export function searchBuyerMatches(id) { return post('/api/matches/search', { sellerId: id }); }
export function createConversation(matchId, buyerId, productId) { return post('/api/conversations', { matchId, buyerId, productId }); }
export function getSellerThread(threadId, signal) { return request(`/api/mock/threads/${encodeURIComponent(threadId)}`, { signal }); }
export async function getSellerLead(leadId, signal) {
  const result = await request(`/api/leads/${encodeURIComponent(leadId)}`, { signal, headers: { 'X-Seller-ID': sellerId } });
  return result.data;
}
