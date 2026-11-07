const endpoint = import.meta.env.VITE_BUYER_POSTS_API_URL?.trim();

export async function createBuyerPost(content) {
  if (!endpoint) return { apiConfigured: false };

  const response = await fetch(endpoint, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      content,
      author: 'Alex Rivera',
      source: 'social-feed-demo',
    }),
  });

  if (!response.ok) throw new Error(`Post request failed (${response.status})`);
  return { apiConfigured: true };
}
