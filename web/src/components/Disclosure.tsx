import type { ReactNode } from 'react';

// A native <details>: keyboard and screen-reader behaviour come from the browser.
export function Disclosure({ summary, children, open }: { summary: ReactNode; children: ReactNode; open?: boolean }) {
  return <details className="disclosure" open={open}><summary>{summary}</summary><div className="disclosure-body">{children}</div></details>;
}
