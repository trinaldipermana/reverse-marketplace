import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

function mockSellerApi() {
  return {
    name: 'mock-seller-api',
    configureServer(server) {
      server.middlewares.use((req, res, next) => {
        const path = new URL(req.url, 'http://localhost').pathname;
        if (req.method !== 'POST' || !['/api/matches/search', '/api/conversations'].includes(path)) {
          next();
          return;
        }

        let body = '';
        req.on('data', (chunk) => { body += chunk; });
        req.on('end', () => {
          const payload = body ? JSON.parse(body) : {};
          res.setHeader('Content-Type', 'application/json');

          if (path === '/api/matches/search') {
            res.statusCode = 200;
            setTimeout(() => res.end(JSON.stringify({
              requestsScanned: 18,
              listingsScanned: 7,
              matches: [{
                matchId: 'match-alex-001',
                threadId: 'alex',
                matchScore: 94,
                matchReasons: ['Beginner friendly', 'Used option', 'Available nearby'],
                buyer: {
                  id: 'buyer-alex',
                  name: 'Alex R.',
                  initials: 'AR',
                  source: 'Marketplace public post',
                  post: 'Searching for a beginner-friendly electronic drum set. Open to used options nearby.',
                  postedAt: '1 hour ago',
                },
                product: {
                  id: 'listing-nitro-max',
                  name: 'Alesis Nitro Max',
                  condition: 'Open box',
                  category: 'Used drum kit',
                  price: 'See Shopee',
                  availability: 'Available now',
                  marketplace: 'Shopee',
                  url: 'https://shopee.co.id/Alesis-Nitro-Max-Electronic-Drum-Kit-Mesh-Head-Bluetooth-%E2%80%93-Set-Drum-Elektrik-Lengkap-i.1408254222.50702962973',
                },
                sellerId: payload.sellerId || 'northside-music',
              }],
            })), 450);
            return;
          }

          res.statusCode = 201;
          res.end(JSON.stringify({
            conversationId: 'conversation-alex-001',
            matchId: payload.matchId,
            status: 'ready',
          }));
        });
      });
    },
  };
}

export default defineConfig({
  plugins: [react(), mockSellerApi()],
});
