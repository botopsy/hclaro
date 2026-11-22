import { readStorage, storageKeys, writeStorage } from '../lib/storage';

const repo = 'https://github.com/botopsy/hclaro';
export const helpLinks = {
  docs: `${repo}#readme`,
  bug: `${repo}/issues/new/choose`,
  star: repo
};

// Only a release build has a GitHub release; development and test builds report other names.
const isRelease = (version?: string) => !!version && /^\d+\.\d+\.\d+/.test(version);

// What's new links to the release notes of the running build, or to the release list.
export const releaseUrl = (version?: string) => isRelease(version) ? `${repo}/releases/tag/v${version}` : `${repo}/releases`;

// Unseen: a release build whose version differs from the last one this browser opened the Help
// menu for. Without storage the dot comes back after a reload.
export const hasUnseenRelease = (version?: string) => isRelease(version) && readStorage('local', storageKeys.seenVersion) !== version;

export function markReleaseSeen(version?: string) {
  if (isRelease(version)) writeStorage('local', storageKeys.seenVersion, version!);
}
