import type { Limits } from './api';

// The values the server serves in /session today; tests that quote a limit read them here.
export const testLimits: Limits = { maxReplace: 50, operations: 100, operationLogs: 10, driftScans: 10, driftMaxAgeHours: 12, graphs: 5, graphMaxAgeHours: 12, graphMaxMebibytes: 16, graphMaxNodes: 20000, graphMaxEdges: 200000 };
