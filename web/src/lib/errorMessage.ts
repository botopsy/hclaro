// errorMessage is the text to show for a failed request: an ApiError already carries the
// server's reason or the UI message for its code.
export function errorMessage(error: unknown, fallback = 'Unable to reach the local Hclaro server. Check that it is still running, then retry.') {
  return error instanceof Error ? error.message : fallback;
}
