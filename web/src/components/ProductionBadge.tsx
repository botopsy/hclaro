import { AlertTriangle } from 'lucide-react';

// A name that looks production-like is a hint only; see productionLike in router.ts.
export function ProductionBadge() {
  return <span className="prod-badge" title="The name looks production-like. Hclaro cannot verify how critical an environment is."><AlertTriangle size={11} />Production-like name</span>;
}
