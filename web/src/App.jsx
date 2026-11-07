import DashboardPage from './features/dashboard/DashboardPage.jsx';
import BuyerDemoPage from './features/buyer-demo/BuyerDemoPage.jsx';

export default function App() {
  return window.location.pathname === '/buyer' ? <BuyerDemoPage /> : <DashboardPage />;
}
