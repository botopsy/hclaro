import { useQuery } from '@tanstack/react-query';
import { api, post, sessionQuery } from '../api';
import type { ContextInfo, Operation, Workspace } from '../types';
import type { Route } from '../router';
import { projectsQuery } from '../hub/HubView';
import { useProfileSelection } from '../profiles/selection';
import { launchScope, scopeKey, scopedPath, snapshotPath, type PlanScope } from '../plans/routes';

// The selected context of one workbench: which project and profile it reads, the Hub context
// it opened, and where its reads, plans and operations go. A Hub workspace reads only through
// its own context ID and the launch project only through its original routes; there is no
// fallback between the two. Queries are keyed by scope, so a late response for a previous
// context or profile never fills the current one.
export function useWorkbenchContext(route: Route, generation: number) {
  const scoped = route.kind === 'context' ? route : undefined;
  // The registry (no engine command) names the current project and the switcher's recent ones.
  const projects = useQuery(projectsQuery);
  const project = projects.data?.projects?.find(p => p.id === scoped?.projectId);
  const launchProject = !scoped ? projects.data?.projects?.find(p => p.launch) : undefined;
  // Execution profiles belong to registered projects. The choice is kept per browser tab.
  const profileProject = scoped ? scoped.projectId : launchProject?.id;
  const profile = useProfileSelection(profileProject);
  const profileReady = !profileProject || profile.profiles.isFetched;
  const opened = useQuery({
    queryKey: ['context-open', scoped?.projectId, scoped?.workspace, profile.selected, generation],
    queryFn: () => post<ContextInfo>('contexts', {
      projectId: scoped!.projectId,
      workspace: scoped!.workspace,
      reload: generation > 0,
      ...(profile.selected ? { profileId: profile.selected } : {})
    }),
    enabled: !!scoped && profileReady
  });
  // Undefined until a Hub workspace's context is open.
  const readScope: PlanScope | undefined = scoped ? opened.data ? { kind: 'context', contextId: opened.data.id } : undefined : launchScope;
  const workspace = useQuery({
    queryKey: ['workspace', readScope ? scopeKey(readScope) : 'none'],
    queryFn: () => api<Workspace>(snapshotPath(readScope!)),
    enabled: !!readScope,
    refetchInterval: q => q.state.data?.status === 'loading' ? 700 : false
  });
  // Plans and applies in a Hub workspace exist only when the server enables them (M24.06);
  // the UI never enables them on its own, and the server refuses them either way.
  const session = useQuery(sessionQuery);
  const hubExecution = !!session.data?.capabilities?.scopedExecution;
  // Where this tab's plans and operations live: the launch project, or only this Hub
  // workspace's own open context.
  const planScope: PlanScope | undefined = scoped ? hubExecution ? readScope : undefined : launchScope;
  const operations = useQuery({
    queryKey: ['operations', planScope ? scopeKey(planScope) : 'none'],
    queryFn: () => api<{ operations: Operation[] }>(scopedPath(planScope!, 'operations')),
    enabled: !!planScope,
    refetchInterval: q => q.state.data?.operations?.some(o => o.status === 'running') ? 2000 : false
  });
  return { projects, project, launchProject, profileProject, profile, opened, readScope, workspace, session, hubExecution, planScope, operations };
}
