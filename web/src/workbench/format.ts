export function providerName(name: string) {
  return name.split('/').at(-1) || 'unknown';
}
export function moduleName(name: string) {
  return name ? name.replace(/^module\./, '') : 'Root module';
}
export function timeLabel(time?: string) {
  return time ? new Date(time).toLocaleTimeString([], {
    hour: '2-digit',
    minute: '2-digit'
  }) : '—';
}
