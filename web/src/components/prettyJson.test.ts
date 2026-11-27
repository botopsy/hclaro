import { expect, it } from 'vitest';
import { prettyJson } from './RedactedLeaf';

it('indents JSON objects and leaves other strings alone', () => {
  expect(prettyJson('{"a":[1]}')).toBe('{\n  "a": [\n    1\n  ]\n}');
  expect(prettyJson('plain')).toBeUndefined();
  expect(prettyJson('{not json')).toBeUndefined();
  expect(prettyJson('123')).toBeUndefined();
});
