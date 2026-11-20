import { useEffect, useState } from 'react';

// useDebounced returns value once it has stopped changing for delay milliseconds.
export function useDebounced<T>(value: T, delay: number) {
  const [settled, setSettled] = useState(value);
  useEffect(() => {
    const timer = window.setTimeout(() => setSettled(value), delay);
    return () => window.clearTimeout(timer);
  }, [value, delay]);
  return settled;
}
