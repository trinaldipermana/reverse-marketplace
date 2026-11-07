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
export async function getSellerInbox(signal) {
  const headers = { 'X-Seller-ID': sellerId };
  const [leadResult, inventoryResult] = await Promise.allSettled([
    request('/api/leads?limit=20', { signal, headers }),
    request('/api/inventory?limit=100', { signal, headers }),
  ]);
  if (leadResult.status === 'rejected') throw leadResult.reason;
  const leads = leadResult.value.data?.items ?? [];
  const items = inventoryResult.status === 'fulfilled' ? inventoryResult.value.data?.items ?? [] : [];
  const itemsById = new Map(items.map((item) => [item.id, item]));
  const threadResults = await Promise.allSettled(leads.map((lead) => getSellerThread(lead.thread_id, signal)));
  const failedThreads = threadResults.filter((result) => result.status === 'rejected').length;
  const threads = leads.map((lead, index) => {
    const history = threadResults[index].status === 'fulfilled' ? threadResults[index].value.messages ?? [] : [];
    const item = itemsById.get(lead.item_id);
    const buyerPost = history.find((message) => message.direction === 'buyer')?.text ?? lead.intent?.item_query ?? 'Buyer request';
    const messages = history.map((message) => ({
      id: message.id,
      from: message.direction === 'agent' ? 'seller' : 'buyer',
      text: message.text,
      time: new Date(message.created_at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }),
    }));
    const latestMessage = history[history.length - 1];
    const stage = lead.state === 'paid' ? 'Purchased'
      : lead.state === 'reserved' ? 'Checkout ready'
        : lead.state === 'lost' ? 'Closed'
          : lead.state === 'escalated' ? 'Needs seller'
            : history.some((message) => message.direction === 'agent') ? 'Conversation active' : 'Waiting for agent';
    const category = item?.attributes?.category || lead.intent?.item_query || 'Buyer request';
    return {
      id: lead.thread_id,
      leadId: lead.id,
      name: lead.buyer_id,
      initials: (lead.buyer_id || 'B').slice(0, 1).toUpperCase(),
      product: item?.name || lead.intent?.item_query || 'Matched product',
      category,
      source: lead.platform,
      time: latestMessage ? new Date(latestMessage.created_at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }) : 'Saved',
      stage,
      unread: false,
      buyerPost,
      listing: item?.name || lead.intent?.item_query || 'Matched product',
      price: item?.list_price_idr == null ? 'Price unavailable' : new Intl.NumberFormat('id-ID', { style: 'currency', currency: 'IDR', maximumFractionDigits: 0 }).format(item.list_price_idr),
      messages,
      events: [],
    };
  });
  return {
    threads,
    warning: inventoryResult.status === 'rejected'
      ? `Saved conversations loaded, but inventory details are unavailable: ${inventoryResult.reason.message}`
      : failedThreads ? `${failedThreads} saved thread${failedThreads === 1 ? '' : 's'} could not be loaded.` : '',
  };
}
export async function getSellerLead(leadId, signal) {
  const result = await request(`/api/leads/${encodeURIComponent(leadId)}`, { signal, headers: { 'X-Seller-ID': sellerId } });
  return result.data;
}
