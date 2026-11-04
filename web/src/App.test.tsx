import { render, screen, waitFor, cleanup } from '@testing-library/react';
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
      canApplySavedPlan: false
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
      name: /main.*aws_subnet/
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
  it('stores only safe appearance settings and keeps future operations disabled', async () => {
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
      name: /Plans/
    })).toBeDisabled();
    expect(screen.getByRole('button', {
      name: /Operations/
    })).toBeDisabled();
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
