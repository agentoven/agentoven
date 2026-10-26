import { BrowserRouter, Routes, Route, Navigate } from 'react-router-dom';
import { Layout } from './components/Layout';
import { AgentsPage } from './pages/Agents';
import { AgentTestPage } from './pages/AgentTest';
import AgentWorkspace from './pages/agent/AgentWorkspace';
import AgentOverview from './pages/agent/Overview';
import AgentConfiguration from './pages/agent/Configuration';
import AgentIngredients from './pages/agent/Ingredients';
import AgentGuardrails from './pages/agent/Guardrails';
import AgentTraces from './pages/agent/AgentTraces';
import { RecipesPage } from './pages/Recipes';
import { PromptsPage } from './pages/Prompts';
import { ProvidersPage } from './pages/Providers';
import { ToolsPage } from './pages/Tools';
import { SkillsPage } from './pages/Skills';
import { TracesPage } from './pages/Traces';
import { TraceDetailPage } from './pages/TraceDetail';
import { OverviewPage } from './pages/Overview';
import { EmbeddingsPage } from './pages/Embeddings';
import { VectorStoresPage } from './pages/VectorStores';
import { RAGPipelinesPage } from './pages/RAGPipelines';
import { ConnectorsPage } from './pages/Connectors';
import { CatalogPage } from './pages/Catalog';
import { PipelineViewPage } from './pages/PipelineView';

function App() {
  return (
    <BrowserRouter>
      <Routes>
        <Route element={<Layout />}>
          <Route path="/" element={<OverviewPage />} />
          <Route path="/agents" element={<AgentsPage />} />
          <Route path="/agents/:name" element={<AgentWorkspace />}>
            <Route index element={<AgentOverview />} />
            <Route path="test" element={<AgentTestPage />} />
            <Route path="configuration" element={<AgentConfiguration />} />
            <Route path="ingredients" element={<AgentIngredients />} />
            <Route path="guardrails" element={<AgentGuardrails />} />
            <Route path="traces" element={<AgentTraces />} />
          </Route>
          <Route path="/recipes" element={<RecipesPage />} />
          <Route path="/dishshelf" element={<PipelineViewPage />} />
          <Route path="/dishshelf/:recipeName" element={<PipelineViewPage />} />
          <Route path="/dishshelf/:recipeName/runs/:runId" element={<PipelineViewPage />} />
          <Route path="/prompts" element={<PromptsPage />} />
          <Route path="/providers" element={<ProvidersPage />} />
          <Route path="/catalog" element={<CatalogPage />} />
          <Route path="/tools" element={<ToolsPage />} />
          <Route path="/skills" element={<SkillsPage />} />
          <Route path="/traces" element={<TracesPage />} />
          <Route path="/traces/:traceId" element={<TraceDetailPage />} />
          <Route path="/embeddings" element={<EmbeddingsPage />} />
          <Route path="/vectorstores" element={<VectorStoresPage />} />
          <Route path="/rag" element={<RAGPipelinesPage />} />
          <Route path="/connectors" element={<ConnectorsPage />} />
          <Route path="*" element={<Navigate to="/" replace />} />
        </Route>
      </Routes>
    </BrowserRouter>
  );
}

export default App;
