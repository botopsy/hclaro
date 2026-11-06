import React from 'react';
import ReactDOM from 'react-dom/client';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { initializeSession } from './api';
import App from './App';
import { consumeDeepLink } from './router';
import './styles.css';
initializeSession();
consumeDeepLink();
const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      retry: false,
      refetchOnWindowFocus: false,
      refetchOnReconnect: false,
      staleTime: Infinity,
      gcTime: 300_000
    }
  }
});
ReactDOM.createRoot(document.getElementById('root')!).render(<React.StrictMode><QueryClientProvider client={queryClient}><App /></QueryClientProvider></React.StrictMode>);
