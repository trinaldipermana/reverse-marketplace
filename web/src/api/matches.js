async function post(path, payload = {}) {
  const response = await fetch(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
  });

  if (!response.ok) {
    throw new Error(`API request failed (${response.status})`);
  }

  return response.json();
}

export function searchBuyerMatches(sellerId) {
  return post('/api/matches/search', { sellerId });
}

export function createConversation(matchId, buyerId, productId) {
  return post('/api/conversations', { matchId, buyerId, productId });
}
