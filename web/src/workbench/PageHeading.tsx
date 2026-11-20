import { ShieldCheck } from 'lucide-react';
import type { Route } from '../router';
import { globalPages, pageOf, type View } from '../navigation/config';

// The title, description and safety badge above every page, from the page tables.
export function PageHeading({
  route,
  view
}: {
  route: Route;
  view: View;
}) {
  const page = route.kind === 'launch' || route.kind === 'context' ? { ...pageOf(view), eyebrow: route.kind === 'context' ? `HUB WORKSPACE · ${route.workspace.toUpperCase()}` : 'YOUR INFRASTRUCTURE, IN FOCUS' } : globalPages[route.kind];
  return <div className="page-heading"><div><div className="eyebrow">{page.eyebrow}</div><h1>{page.title}</h1><p>{page.description}</p></div><span className="read-only-badge"><ShieldCheck size={13} />{page.badge}</span></div>;
}
