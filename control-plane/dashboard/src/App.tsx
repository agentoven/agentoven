import { BrowserRouter, Routes, Route, Navigate } from 'react-router-dom';
import { Layout } from './components/Layout';
import { AgentsPage } from './pages/Agents';
import { RecipesPage } from './pages/Recipes';
import { ProvidersPage } from './pages/Providers';
import { ToolsPage } from './pages/Tools';
import { TracesPage } from './pages/Traces';
import { OverviewPage } from './pages/Overview';

function App() {
  return (
    <BrowserRouter>
      <Routes>
        <Route element={<Layout />}>
          <Route path="/" element={<OverviewPage />} />
          <Route path="/agents" element={<AgentsPage />} />
          <Route path="/recipes" element={<RecipesPage />} />
          <Route path="/providers" element={<ProvidersPage />} />
          <Route path="/tools" element={<ToolsPage />} />
          <Route path="/traces" element={<TracesPage />} />
          <Route path="*" element={<Navigate to="/" replace />} />
        </Route>
      </Routes>
    </BrowserRouter>
  );
}

export default App;
