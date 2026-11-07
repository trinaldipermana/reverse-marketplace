import DashboardPage from './features/dashboard/DashboardPage.jsx';
import BuyerDemoPage from './features/buyer-demo/BuyerDemoPage.jsx';
import HackathonDemoPage from './features/hackathon/HackathonDemoPage.jsx';

export default function App() {
  if (window.location.pathname === '/buyer') return <BuyerDemoPage />;
  if (window.location.pathname === '/hackathon') return <HackathonDemoPage />;
  return <DashboardPage />;
}
