import { render, screen, waitFor, within, cleanup } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import App from './App';
import type { Resource, Workspace } from './types';
import { JsonTree } from './components/JsonTree';
const resource: Resource = {
  id: 'test',
  address: 'module.network.aws_subnet.main[0]',
  mode: 'managed',
  type: 'aws_subnet',
  name: 'main',
  provider: 'registry.terraform.io/hashicorp/aws',
  module: 'module.network',
  index: 0,
  attributes: {
    id: 'subnet-demo',
    password: '[redacted]',
    nested: {
      name: 'example'
    }
  }
};
const workspace: Workspace = {
  status: 'ready',
  capturedAt: '2026-11-04T12:00:00Z',
  info: {
    project: 'demo',
    engine: 'terraform',
    version: '1.9.8',
    workspace: 'demo',
    backend: 'local',
    executionMode: 'local-cli',
    initialized: true,
    providerLock: true,
    capabilities: {
      canReadState: true,
      canPlan: false,
      canApplySavedPlan: false,
      supportsImportBlocks: true,
      supportsGeneratedConfig: true,
      canImport: false
    },
    warnings: []
  },
  managed: 1,
  data: 0,
  modules: [{
    address: '',
    parent: '',
    count: 0
  }, {
    address: 'module.network',
    parent: '',
    count: 1
  }],
  providers: {
    [resource.provider]: 1
  },
  types: {
    aws_subnet: 1
  }
};
function app() {
  return render(<QueryClientProvider client={new QueryClient({
    defaultOptions: {
      queries: {
        retry: false
      }
    }
  })}><App /></QueryClientProvider>);
}
beforeEach(() => {
  localStorage.clear();
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    if (url.endsWith('workspace')) return Response.json(workspace);
    if (url.endsWith('operations')) return Response.json({ operations: [] });
    if (url.includes('resources/test')) return Response.json(resource);
    return Response.json({
      resources: [resource],
      total: 1
    });
  }));
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
describe('Resource explorer', () => {
  it('renders API resources, opens an inspector, and uses only redacted attributes', async () => {
    app();
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', {
      name: resource.address
    }));
    expect(await screen.findByLabelText('Resource inspector')).toBeInTheDocument();
    expect(await screen.findByText('subnet-demo')).toBeInTheDocument();
    expect(screen.getByText('redacted')).toBeInTheDocument();
    expect(document.body).not.toHaveTextContent('DO_NOT_LEAK_HCLARO_TEST_SECRET');
    await user.click(screen.getByRole('button', {
      name: 'Copy resource address'
    }));
    expect(await screen.findByRole('status')).toHaveTextContent('Address copied');
    await user.keyboard('{Escape}');
    expect(screen.queryByLabelText('Resource inspector')).not.toBeInTheDocument();
  });
  it('searches full addresses with bounded paging and composes filters', async () => {
    app();
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText('Search resources'), 'aws_subnet');
    await waitFor(() => expect(fetch).toHaveBeenCalledWith(expect.stringContaining('query=aws_subnet'), expect.anything()));
    await user.selectOptions(screen.getByLabelText('Filter resource mode'), 'data');
    await waitFor(() => expect(fetch).toHaveBeenCalledWith(expect.stringContaining('mode=data'), expect.anything()));
  });
  it('shows one-line rows with the latest drift scan, Group by and a module filter instead of a tree', async () => {
    const other: Resource = { ...resource, id: 'other', address: 'aws_vpc.main', type: 'aws_vpc', module: '', index: undefined };
    const scan = { id: 's', status: 'completed', exhaustive: true, current: true, withheld: false, resources: [{ id: 'c', resourceId: 'test', classification: 'changed' }] };
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      if (url.endsWith('workspace')) return Response.json(workspace);
      if (url.endsWith('operations')) return Response.json({ operations: [] });
      if (url.endsWith('drift-scans')) return Response.json({ scans: [scan] });
      if (url.includes('resources/test')) return Response.json(resource);
      return Response.json({ resources: [resource, other], total: 2 });
    }));
    app();
    const user = userEvent.setup();
    const table = await screen.findByRole('table', { name: 'Resources' });
    expect(screen.queryByRole('complementary', { name: 'Module navigation' })).not.toBeInTheDocument();
    const row = (address: string) => within(table).getByRole('button', { name: address }).closest('tr')!;
    await waitFor(() => expect(row(resource.address)).toHaveTextContent('Drifted'));
    expect(row(other.address)).toHaveTextContent('No drift');
    // Module grouping is the default; group rows collapse.
    const group = within(table).getByRole('button', { name: /^module\.network\s*1$/ });
    await user.click(group);
    expect(group).toHaveAttribute('aria-expanded', 'false');
    expect(within(table).queryByRole('button', { name: resource.address })).not.toBeInTheDocument();
    await user.click(within(screen.getByRole('group', { name: 'Group by' })).getByRole('button', { name: 'None' }));
    expect(within(table).queryByRole('button', { name: /^module\.network\s*1$/ })).not.toBeInTheDocument();
    await user.selectOptions(screen.getByLabelText('Filter module'), 'module.network');
    await waitFor(() => expect(fetch).toHaveBeenCalledWith(expect.stringContaining('module=module.network'), expect.anything()));
    // The drawer shows the same drift result and closes on a backdrop click.
    await user.click(await screen.findByRole('button', { name: resource.address }));
    const drawer = await screen.findByLabelText('Resource inspector');
    expect(within(drawer).getByText('Drifted')).toBeInTheDocument();
    await user.click(document.querySelector('.drawer-backdrop')!);
    expect(screen.queryByLabelText('Resource inspector')).not.toBeInTheDocument();
  });
  it('says Not checked when no current scan exists', async () => {
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      if (url.endsWith('workspace')) return Response.json(workspace);
      if (url.endsWith('operations')) return Response.json({ operations: [] });
      if (url.endsWith('drift-scans')) return Response.json({ scans: [{ id: 's', status: 'completed', exhaustive: true, current: false, withheld: true, resources: [] }] });
      return Response.json({ resources: [resource], total: 1 });
    }));
    app();
    const table = await screen.findByRole('table', { name: 'Resources' });
    await waitFor(() => expect(fetch).toHaveBeenCalledWith(expect.stringContaining('drift-scans'), expect.anything()));
    expect(within(table).getByText('Not checked')).toBeInTheDocument();
  });
  it('distinguishes an unavailable workspace from an empty state', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => Response.json({
      ...workspace,
      status: 'error',
      diagnostic: {
        code: 'uninitialized',
        message: 'Run init yourself.'
      }
    })));
    app();
    expect(await screen.findByRole('alert')).toHaveTextContent('Initialize this workspace first');
    expect(screen.queryByText('A clean slate')).not.toBeInTheDocument();
  });
  it('names a state lock held by another process and picks up the automatic re-read without a click', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      let reads = 0;
      vi.stubGlobal('fetch', vi.fn(async (url: string) => {
        if (url.endsWith('/session')) return Response.json({ csrfToken: 'c'.repeat(64) });
        if (url.endsWith('workspace')) {
          reads++;
          return Response.json(reads === 1 ? { ...workspace, status: 'error', diagnostic: { code: 'state_locked', message: 'Another process holds the state lock, so the state could not be read.' } } : workspace);
        }
        if (url.endsWith('operations')) return Response.json({ operations: [] });
        return Response.json({ resources: [resource], total: 1 });
      }));
      app();
      const alert = await screen.findByRole('alert');
      expect(alert).toHaveTextContent('Another process holds the state lock');
      expect(alert).not.toHaveTextContent('credentials');
      expect(screen.getByRole('heading', { name: 'Another process holds the state lock' })).toBeInTheDocument();
      expect(screen.getAllByRole('button', { name: /Read again/ }).length).toBeGreaterThan(0);
      await vi.advanceTimersByTimeAsync(5500);
      await waitFor(() => expect(screen.queryByRole('alert')).not.toBeInTheDocument());
      expect(reads).toBeGreaterThanOrEqual(2);
    } finally {
      vi.useRealTimers();
    }
  });
  it('reads the terminal-opened project again after it was initialized, without running init or plan', async () => {
    let initialized = false;
    const posts: { url: string; body: unknown }[] = [];
    vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit = {}) => {
      if (init.method === 'POST') {
        posts.push({ url, body: JSON.parse(init.body as string) });
        initialized = true;
        return Response.json({ status: 'loading' }, { status: 202 });
      }
      if (url.endsWith('/session')) return Response.json({ csrfToken: 'c'.repeat(64) });
      if (url.endsWith('workspace')) return Response.json(initialized ? workspace : { ...workspace, status: 'error', diagnostic: { code: 'uninitialized', message: 'Backend initialization was not detected. Read the project again after init.' } });
      if (url.endsWith('operations')) return Response.json({ operations: [] });
      return Response.json({ resources: [resource], total: 1 });
    }));
    app();
    const user = userEvent.setup();
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('Initialize this workspace first');
    // One icon button in the top bar and one on the error screen.
    expect(screen.getByRole('button', { name: 'Read state again' })).toBeInTheDocument();
    expect(screen.getAllByRole('button', { name: 'Read again' })).toHaveLength(1);
    await user.click(within(alert).getByRole('button', { name: 'Read again' }));
    expect(await screen.findByRole('button', { name: resource.address })).toBeInTheDocument();
    expect(posts).toEqual([{ url: '/api/v1/workspace/reload', body: {} }]);
  });
  it('opens Drift without scanning and offers a separate normal plan', async () => {
    const posts: string[] = [];
    vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit = {}) => {
      if (init.method === 'POST') posts.push(url);
      if (url.endsWith('workspace')) return Response.json(workspace);
      if (url.endsWith('operations')) return Response.json({ operations: [] });
      if (url.includes('drift-scans/options')) return Response.json({ canScan: true, engine: 'terraform', version: '1.9.8', workspace: 'demo', backend: 'local', executionMode: 'local-cli', varFiles: [] });
      if (url.endsWith('drift-scans')) return Response.json({ scans: [] });
      if (url.includes('plans/options')) return Response.json({ canPlan: false, reason: 'Not here.', canApply: false, engine: 'terraform', version: '1.9.8', workspace: 'demo', backend: 'local', varFiles: [] });
      if (url.endsWith('plans')) return Response.json({ plans: [] });
      return Response.json({ resources: [resource], total: 1 });
    }));
    app();
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: 'Drift detection' }));
    expect(await screen.findByRole('heading', { name: 'Drift detection' })).toBeInTheDocument();
    expect(await screen.findByText('No drift scans yet')).toBeInTheDocument();
    expect(posts).toEqual([]);
  });
  it('shows empty and no-match states without inventing resources', async () => {
    vi.stubGlobal('fetch', vi.fn(async (url: string) => Response.json(url.endsWith('workspace') ? {
      ...workspace,
      status: 'empty',
      managed: 0
    } : {
      resources: [],
      total: 0
    })));
    app();
    expect(await screen.findByText('A clean slate')).toBeInTheDocument();
  });
  it('copies any selection and plans a replacement of the managed instances only', async () => {
    const data: Resource = { ...resource, id: 'data', address: 'data.aws_caller_identity.current', mode: 'data', type: 'aws_caller_identity', name: 'current' };
    const ready = { ...workspace, info: { ...workspace.info, capabilities: { canReadState: true, canPlan: true, canApplySavedPlan: true } } };
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      if (url.endsWith('workspace')) return Response.json(ready);
      if (url.endsWith('operations')) return Response.json({ operations: [] });
      if (url.endsWith('plans/options')) return Response.json({ canPlan: true, canApply: true, engine: 'terraform', version: '1.9.8', workspace: 'demo', backend: 'local', varFiles: [] });
      if (url.endsWith('plans')) return Response.json({ plans: [] });
      return Response.json({ resources: [resource, data], total: 2 });
    }));
    app();
    const user = userEvent.setup();
    // A data source alone can be copied but never planned for replacement.
    await user.click(await screen.findByLabelText(`Select ${data.address}`));
    const bar = screen.getByRole('region', { name: 'Selection' });
    expect(bar).toHaveTextContent('1 selected');
    expect(within(bar).getByRole('button', { name: /Plan replacement/ })).toBeDisabled();
    await user.click(screen.getByLabelText(`Select ${resource.address}`));
    expect(bar).toHaveTextContent('2 selected');
    await user.click(within(bar).getByRole('button', { name: 'Copy addresses' }));
    expect(await navigator.clipboard.readText()).toBe(`${data.address}\n${resource.address}`);
    await user.click(within(bar).getByRole('button', { name: 'Copy -target flags' }));
    expect(await navigator.clipboard.readText()).toBe(`'-target=${data.address}' '-target=${resource.address}'`);
    await user.click(within(bar).getByRole('button', { name: /Plan replacement/ }));
    const dialog = await screen.findByRole('dialog', { name: 'Plan a replacement' });
    expect(dialog).toHaveTextContent(resource.address);
    expect(dialog).not.toHaveTextContent(data.address);
    // The footer and its safety line are gone (redesign §2.2).
    expect(screen.queryByRole('contentinfo')).not.toBeInTheDocument();
  });
  it('shows the hollow-cursor brand once, with one accessible name', async () => {
    app();
    const home = screen.getByRole('link', { name: 'Hclaro overview' });
    // The wordmark and the compact mark (shown in a collapsed sidebar) are both decorative here.
    const wordmark = home.querySelector('.hclaro-logo')!;
    expect(wordmark).toHaveAttribute('aria-hidden', 'true');
    expect(wordmark).toHaveTextContent(/^hclaro$/);
    expect(wordmark.querySelector('svg polyline')).toHaveAttribute('points', '2,2 8,7 2,12');
    expect(wordmark.querySelector('.hclaro-cursor')).not.toBeNull();
    expect(home.querySelector('.hclaro-mark.brand-compact')).toHaveAttribute('aria-hidden', 'true');
    expect(within(home).queryByRole('img')).not.toBeInTheDocument();
    expect(home.querySelector('.brand-mark')).toBeNull();
    const user = userEvent.setup();
    await user.click(screen.getByRole('button', { name: 'Settings' }));
    expect(await screen.findByRole('img', { name: 'Hclaro' })).toHaveClass('hclaro-auto');
  });
  it('stores only safe appearance settings and offers no apply workflow', async () => {
    app();
    const user = userEvent.setup();
    await user.click(screen.getByRole('button', {
      name: 'Settings'
    }));
    await user.click(await screen.findByRole('button', {
      name: 'light'
    }));
    expect(document.documentElement.dataset.theme).toBe('light');
    expect(JSON.parse(localStorage.getItem('hclaro.preferences')!).theme).toBe('light');
    expect(screen.getByRole('button', {
      name: 'Plans'
    })).toBeEnabled();
    expect(screen.queryByRole('button', {
      name: /apply/i
    })).not.toBeInTheDocument();
  });
  it('opens Resources grouped by module from a module card, and by type from a provider card', async () => {
    app();
    const user = userEvent.setup();
    await screen.findByRole('table', { name: 'Resources' });
    await user.click(screen.getByRole('button', { name: 'Modules' }));
    await user.click(within(await screen.findByRole('article', { name: 'network' })).getByRole('button', { name: 'View resources →' }));
    expect(await screen.findByLabelText('Search resources')).toHaveValue('module.network.');
    await waitFor(() => expect(fetch).toHaveBeenCalledWith(expect.stringContaining('query=module.network.'), expect.anything()));
    expect(within(screen.getByRole('group', { name: 'Group by' })).getByRole('button', { name: 'Module' })).toHaveAttribute('aria-pressed', 'true');
    await user.click(screen.getByRole('button', { name: 'Providers' }));
    await user.click(within(await screen.findByRole('article', { name: 'aws' })).getByRole('button', { name: 'View resources →' }));
    expect(await screen.findByLabelText('Search resources')).toHaveValue('');
    await waitFor(() => expect(fetch).toHaveBeenCalledWith(expect.stringContaining('provider=registry.terraform.io'), expect.anything()));
    expect(within(screen.getByRole('group', { name: 'Group by' })).getByRole('button', { name: 'Type' })).toHaveAttribute('aria-pressed', 'true');
  });
});
it('renders hostile JSON as text', () => {
  render(<JsonTree value={{
    id: '<img src=x onerror=alert(1)>',
    unknown: '[redacted]'
  }} />);
  expect(document.querySelector('img')).toBeNull();
  expect(screen.getByText('<img src=x onerror=alert(1)>')).toBeInTheDocument();
});
