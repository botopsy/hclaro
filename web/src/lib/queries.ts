import { queryOptions } from '@tanstack/react-query';
import { api, send } from '../api';
import { operationActive, type Operation, type ProjectList } from '../types';
import { scopeKey, scopedPath, type PlanScope } from './scope';

type OperationList = { operations: Operation[] };

// operationsQuery lists a scope's operations (none when scope is undefined) and polls while any
// is queued, running or cancelling, so a queued operation moves on without a reload.
export function operationsQuery(scope: PlanScope | undefined) {
  return queryOptions({
    queryKey: ['operations', scope ? scopeKey(scope) : 'none'],
    queryFn: () => api<OperationList>(scopedPath(scope!, 'operations')),
    enabled: !!scope,
    refetchInterval: q => q.state.data?.operations?.some(operationActive) ? 2000 : false
  });
}

// activeOperation is the operation a view follows: the newest queued, running or cancelling one.
export function activeOperation(list: Operation[] | undefined) {
  return list?.find(operationActive);
}

export const projectsQuery = {
  queryKey: ['projects'],
  queryFn: () => api<ProjectList>('projects')
};

// revealValue asks the server for one redacted scalar of a resource (M30). It is a plain call,
// not a query: the value must never enter the query cache.
export async function revealValue(scope: PlanScope, resourceId: string, path: (string | number)[]) {
  const body = await send<{ value: unknown }>('POST', scopedPath(scope, `resources/${resourceId}/reveal`), { path });
  return String(body.value);
}
