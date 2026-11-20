// Display formatting shared by every feature. A missing time shows as "—".

// engineLabel names the IaC engine; an unknown or not-yet-known engine reads as fallback.
export function engineLabel(engine?: string, fallback = 'The engine') {
  return engine === 'tofu' ? 'OpenTofu' : engine === 'terraform' ? 'Terraform' : fallback;
}
export function providerName(name: string) {
  return name.split('/').at(-1) || 'unknown';
}
export function moduleName(name: string) {
  return name ? name.replace(/^module\./, '') : 'Root module';
}
// timeLabel is hours and minutes; clock adds seconds.
export function timeLabel(time?: string) {
  return time ? new Date(time).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }) : '—';
}
export function clock(time?: string) {
  return time ? new Date(time).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' }) : '—';
}
// dayTime is a short date and time ("Nov 5, 10:00 AM"); dateTime is the medium date with it.
export function dayTime(time?: string) {
  return time ? new Date(time).toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' }) : '—';
}
export function dateTime(time?: string) {
  return time ? new Date(time).toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' }) : '—';
}
export function duration(ms: number) {
  const seconds = Math.round(ms / 1000);
  return seconds < 60 ? `${seconds}s` : `${Math.floor(seconds / 60)}m ${seconds % 60}s`;
}
